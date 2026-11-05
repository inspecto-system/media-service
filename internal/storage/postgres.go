// Package storage connects the service to PostgreSQL and keeps the schema up to date.
package storage

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	// Registers the pgx5:// scheme used by golang-migrate.
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Connect opens a connection pool and checks that the database answers.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("configure database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

// Migrate applies the embedded migrations. url must use the role that owns the schema; the
// service itself connects with a DML-only role (SECURITY.md §6).
func Migrate(url string) (err error) {
	source, err := iofs.New(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", source, toMigrateURL(url))
	if err != nil {
		return fmt.Errorf("open database for migrations: %w", err)
	}
	defer func() {
		sourceErr, dbErr := m.Close()
		if closeErr := errors.Join(sourceErr, dbErr); closeErr != nil && err == nil {
			err = fmt.Errorf("close migrations: %w", closeErr)
		}
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// toMigrateURL switches a postgres:// URL to the pgx5:// scheme of the golang-migrate driver.
func toMigrateURL(url string) string {
	for _, scheme := range []string{"postgresql://", "postgres://"} {
		if rest, ok := strings.CutPrefix(url, scheme); ok {
			return "pgx5://" + rest
		}
	}
	return url
}
