// Package testsupport starts real dependencies for integration tests (CODE_QUALITY.md §4: no
// database mocks). It is imported only by tests.
package testsupport

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/inspecto-system/media-service/internal/config"
)

const (
	image     = "postgres:16-alpine"
	superuser = "postgres"
	database  = "inspecto_media"
)

// roles creates the two roles of platform/deploy/postgres/init/01-databases.sh and hands the
// database over to the migrator, so tests run with the same privileges as the deployment.
func roles(migratorPass, appPass string) []string {
	return []string{
		"CREATE ROLE media_migrator LOGIN PASSWORD '" + migratorPass + "'",
		"CREATE ROLE media_app LOGIN PASSWORD '" + appPass + "'",
		"ALTER DATABASE " + database + " OWNER TO media_migrator",
		"REVOKE ALL ON DATABASE " + database + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + database + " TO media_app",
		"REVOKE ALL ON SCHEMA public FROM PUBLIC",
		"GRANT USAGE ON SCHEMA public TO media_app",
		"ALTER DEFAULT PRIVILEGES FOR ROLE media_migrator IN SCHEMA public " +
			"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO media_app",
	}
}

// Postgres is a running test database.
type Postgres struct {
	// Config points to the database with the migrator and app roles.
	Config config.Database
}

// StartPostgres starts PostgreSQL in a container that is removed when the test ends.
func StartPostgres(t *testing.T) Postgres {
	t.Helper()
	db, stop, err := RunPostgres(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return db
}

// RunPostgres starts PostgreSQL for a whole test package (from TestMain); call stop at the end.
func RunPostgres(ctx context.Context) (Postgres, func(), error) {
	container, err := postgres.Run(ctx, image,
		postgres.WithDatabase(database),
		postgres.WithUsername(superuser),
		postgres.WithPassword(rand.Text()),
		postgres.BasicWaitStrategies())
	stop := func() { _ = testcontainers.TerminateContainer(container) }
	if err != nil {
		stop()
		return Postgres{}, nil, fmt.Errorf("start PostgreSQL: %w", err)
	}
	db, err := prepare(ctx, container)
	if err != nil {
		stop()
		return Postgres{}, nil, err
	}
	return db, stop, nil
}

func prepare(ctx context.Context, container *postgres.PostgresContainer) (Postgres, error) {
	superuserURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return Postgres{}, fmt.Errorf("connection string: %w", err)
	}
	// Passwords are generated per run: the container is disposable and no secret is kept in code.
	migratorPass, appPass := rand.Text(), rand.Text()
	if err := createRoles(ctx, superuserURL, roles(migratorPass, appPass)); err != nil {
		return Postgres{}, err
	}
	host, err := container.Host(ctx)
	if err != nil {
		return Postgres{}, fmt.Errorf("host: %w", err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return Postgres{}, fmt.Errorf("port: %w", err)
	}
	return Postgres{Config: config.Database{
		URL:      "postgres://" + host + ":" + port.Port() + "/" + database + "?sslmode=disable",
		App:      config.Credentials{User: "media_app", Password: appPass},
		Migrator: config.Credentials{User: "media_migrator", Password: migratorPass},
	}}, nil
}

func createRoles(ctx context.Context, superuserURL string, statements []string) error {
	conn, err := pgx.Connect(ctx, superuserURL)
	if err != nil {
		return fmt.Errorf("connect as superuser: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}

// URL returns the database URL for a role, failing the test on error.
func (p Postgres) URL(t *testing.T, role config.Credentials) string {
	t.Helper()
	url, err := p.Config.URLFor(role)
	if err != nil {
		t.Fatalf("URLFor: %v", err)
	}
	return url
}
