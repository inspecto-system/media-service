# media-service

Сервис фотографий Inspecto (Go): приём, проверка, сжатие, хранение и выдача фото (ADR-002).
Документация проекта, контракт API (`api/media.yaml`) и окружение разработки — в репозитории
[platform](https://github.com/inspecto-system/platform).

## Структура

| Путь | Назначение |
|---|---|
| `cmd/media-service` | Точка входа: конфигурация, сигналы остановки |
| `internal/app` | Сборка сервиса: миграции, зависимости, HTTP-сервер, фоновая очистка, плавная остановка |
| `internal/auth` | Проверка JWT пользователей по JWKS core-service (только RS256, `iss`, `aud`, `exp`) |
| `internal/config` | Настройки из переменных окружения |
| `internal/core` | Внутренний API core: сервисный токен, проверка доступа к объекту (кеш положительных ответов 60 с) |
| `internal/httpapi` | Маршрутизация, `/api/v1/photos`, `/internal/v1/photos` (сервисные токены со scope `photos.manage` / `photos.read`), `X-Request-Id`, журнал запросов, ошибки RFC 9457, `/health/*` |
| `internal/imaging` | Проверка по сигнатуре, лимит пикселей до декодирования, поворот по EXIF, три варианта JPEG (ADR-025) |
| `internal/jobs` | Удаление фото, не прикреплённых за `MEDIA_UNATTACHED_TTL` (ADR-018) |
| `internal/logging` | JSON-логи с полями ECS, как у core-service |
| `internal/objectstore` | Файлы в S3-совместимом хранилище (SeaweedFS, ADR-024) |
| `internal/service` | Правила загрузки, выдачи и удаления фото; внутренние операции: метаданные, attach, удаление (сначала файлы, потом строки — повтор безопасен) |
| `internal/storage` | PostgreSQL (pgx), таблица `photos`, миграции (встроены в бинарник) |
| `internal/testsupport` | PostgreSQL и SeaweedFS в Testcontainers, выпуск тестовых JWT |

## Настройки

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `MEDIA_PORT` | `8082` | HTTP-порт |
| `MEDIA_DB_URL` | `postgres://127.0.0.1:5432/inspecto_media?sslmode=require` | База без логина и пароля |
| `MEDIA_DB_APP_USER` / `MEDIA_DB_APP_PASSWORD` | `media_app` / — | Роль приложения (только DML) |
| `MEDIA_DB_MIGRATOR_USER` / `MEDIA_DB_MIGRATOR_PASSWORD` | `media_migrator` / — | Владелец схемы, только для миграций |
| `MEDIA_JWKS_URL` | `http://127.0.0.1:8081/api/v1/.well-known/jwks.json` | Открытые ключи core |
| `MEDIA_JWT_ISSUER` / `MEDIA_JWT_AUDIENCE` | `inspecto-core` / `inspecto-api` | Ожидаемые `iss` и `aud` токена |
| `MEDIA_JWT_INTERNAL_AUDIENCE` | `inspecto-internal` | `aud` сервисных токенов, принимаются только на `/internal/**` |
| `MEDIA_CORE_URL` | `http://127.0.0.1:8081` | core-service для проверки доступа |
| `MEDIA_CORE_CLIENT_SECRET` | — | Секрет клиента `media-service` (тот же, что `CORE_MEDIA_CLIENT_SECRET` в core) |
| `MEDIA_S3_ENDPOINT` / `MEDIA_S3_REGION` / `MEDIA_S3_BUCKET` | `http://127.0.0.1:8333` / `us-east-1` / `photos` | Хранилище файлов |
| `MEDIA_S3_ACCESS_KEY` / `MEDIA_S3_SECRET_KEY` | — | S3-идентичность media-service |
| `MEDIA_MAX_FILE_SIZE_BYTES` | `15728640` (15 МБ) | Предельный размер файла (`413`) |
| `MEDIA_MAX_PIXELS` | `50000000` | Предельное число пикселей, проверяется до декодирования |
| `MEDIA_MAX_PHOTOS_PER_OBSERVATION` | `10` | Публикуется в `/photos/limits`, соблюдается core при привязке |
| `MEDIA_IMAGE_WORKERS` | `2` | Одновременных декодирований (50 Мпикс ≈ 200 МБ памяти) |
| `MEDIA_UNATTACHED_TTL` | `24h` | Через сколько удаляется неприкреплённое фото |

Пароли и секреты обязательны и передаются только через окружение (SECURITY.md §5); сервис сообщает обо всех отсутствующих сразу.

## Проверки

Go на компьютере не нужен: инструменты запускаются в закреплённых Docker-образах. С `make` (Linux, WSL):

```bash
make check   # golangci-lint, сборка, тесты с -race и порогом покрытия, govulncheck
```

Без `make` (Git Bash в Windows) — те же команды напрямую:

```bash
export MSYS_NO_PATHCONV=1
docker run --rm -v "$(pwd -W):/src" -v inspecto-gomod:/go/pkg/mod -w /src golangci/golangci-lint:v2.15.0 golangci-lint run ./...
docker run --rm -v "$(pwd -W):/src" -v inspecto-gomod:/go/pkg/mod -v /var/run/docker.sock:/var/run/docker.sock -e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal -e TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock -w /src golang:1.27.3 go test -race -cover ./...
```

## Запуск против dev-окружения

PostgreSQL и SeaweedFS из `platform/deploy` (пароли — из `deploy/.env`) и запущенный core-service с `CORE_MEDIA_CLIENT_SECRET`:

```bash
docker compose -f ../platform/deploy/compose.dev.yaml --env-file ../platform/deploy/.env up -d --wait postgres s3
docker compose -f ../platform/deploy/compose.dev.yaml --env-file ../platform/deploy/.env up s3-init
set -a; . ../platform/deploy/.env; set +a
docker run --rm -p 127.0.0.1:8082:8082 -v "$(pwd -W):/src" -v inspecto-gomod:/go/pkg/mod -w /src \
  -e MEDIA_DB_URL="postgres://host.docker.internal:${POSTGRES_PORT}/inspecto_media?sslmode=require" \
  -e MEDIA_DB_APP_PASSWORD="$MEDIA_APP_PASSWORD" -e MEDIA_DB_MIGRATOR_PASSWORD="$MEDIA_MIGRATOR_PASSWORD" \
  -e MEDIA_JWKS_URL=http://host.docker.internal:8081/api/v1/.well-known/jwks.json \
  -e MEDIA_CORE_URL=http://host.docker.internal:8081 -e MEDIA_CORE_CLIENT_SECRET="$CORE_MEDIA_CLIENT_SECRET" \
  -e MEDIA_S3_ENDPOINT=http://host.docker.internal:8333 \
  -e MEDIA_S3_ACCESS_KEY="$S3_MEDIA_ACCESS_KEY" -e MEDIA_S3_SECRET_KEY="$S3_MEDIA_SECRET_KEY" \
  golang:1.27.3 go run ./cmd/media-service
```

Проверка: `curl http://127.0.0.1:8082/health/ready` → `{"status":"UP"}`.
