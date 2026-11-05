# media-service

Сервис фотографий Inspecto (Go): приём, проверка, сжатие, хранение и выдача фото (ADR-002).
Документация проекта, контракт API (`api/media.yaml`) и окружение разработки — в репозитории
[platform](https://github.com/inspecto-system/platform).

## Структура

| Путь | Назначение |
|---|---|
| `cmd/media-service` | Точка входа: конфигурация, сигналы остановки |
| `internal/app` | Сборка сервиса: миграции, пул соединений, HTTP-сервер, плавная остановка |
| `internal/config` | Настройки из переменных окружения |
| `internal/httpapi` | Маршрутизация, `X-Request-Id`, журнал запросов, ошибки RFC 9457, `/health/*` |
| `internal/logging` | JSON-логи с полями ECS, как у core-service |
| `internal/storage` | PostgreSQL (pgx) и миграции (golang-migrate, файлы встроены в бинарник) |
| `internal/testsupport` | PostgreSQL в Testcontainers для интеграционных тестов |

## Настройки

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `MEDIA_PORT` | `8082` | HTTP-порт |
| `MEDIA_DB_URL` | `postgres://127.0.0.1:5432/inspecto_media?sslmode=require` | База без логина и пароля |
| `MEDIA_DB_APP_USER` / `MEDIA_DB_APP_PASSWORD` | `media_app` / — | Роль приложения (только DML) |
| `MEDIA_DB_MIGRATOR_USER` / `MEDIA_DB_MIGRATOR_PASSWORD` | `media_migrator` / — | Владелец схемы, только для миграций |

Пароли обязательны и передаются только через окружение (SECURITY.md §5).

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

PostgreSQL из `platform/deploy` (пароли — из `deploy/.env`):

```bash
docker compose -f ../platform/deploy/compose.dev.yaml --env-file ../platform/deploy/.env up -d --wait postgres
set -a; . ../platform/deploy/.env; set +a
docker run --rm -p 127.0.0.1:8082:8082 -v "$(pwd -W):/src" -v inspecto-gomod:/go/pkg/mod -w /src \
  -e MEDIA_DB_URL="postgres://host.docker.internal:${POSTGRES_PORT}/inspecto_media?sslmode=require" \
  -e MEDIA_DB_APP_PASSWORD="$MEDIA_APP_PASSWORD" -e MEDIA_DB_MIGRATOR_PASSWORD="$MEDIA_MIGRATOR_PASSWORD" \
  golang:1.27.3 go run ./cmd/media-service
```

Проверка: `curl http://127.0.0.1:8082/health/ready` → `{"status":"UP"}`.
