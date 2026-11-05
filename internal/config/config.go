// Package config reads the media-service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// Config is the complete runtime configuration of the service.
type Config struct {
	// Port is the HTTP port the service listens on.
	Port int
	// Database holds the PostgreSQL connection settings.
	Database Database
}

// Database describes the connection to the inspecto_media database. Runtime queries use the
// DML-only App role; migrations use the Migrator role that owns the schema (SECURITY.md §6).
type Database struct {
	// URL is a postgres:// URL without credentials, for example
	// postgres://127.0.0.1:5432/inspecto_media?sslmode=require.
	URL      string
	App      Credentials
	Migrator Credentials
}

// Credentials of a database role.
type Credentials struct {
	User     string
	Password string
}

// String hides the password, so credentials can never end up in logs by accident.
func (c Credentials) String() string {
	return c.User + ":***"
}

const (
	defaultPort         = 8082
	defaultDatabaseURL  = "postgres://127.0.0.1:5432/inspecto_media?sslmode=require"
	defaultAppUser      = "media_app"
	defaultMigratorUser = "media_migrator"
)

// Load builds the configuration from the given lookup function (os.Getenv in production).
func Load(getenv func(string) string) (Config, error) {
	port, err := parsePort(valueOr(getenv("MEDIA_PORT"), strconv.Itoa(defaultPort)))
	if err != nil {
		return Config{}, err
	}
	dbURL := valueOr(getenv("MEDIA_DB_URL"), defaultDatabaseURL)
	if err := checkDatabaseURL(dbURL); err != nil {
		return Config{}, err
	}
	cfg := Config{
		Port: port,
		Database: Database{
			URL: dbURL,
			App: Credentials{
				User:     valueOr(getenv("MEDIA_DB_APP_USER"), defaultAppUser),
				Password: getenv("MEDIA_DB_APP_PASSWORD"),
			},
			Migrator: Credentials{
				User:     valueOr(getenv("MEDIA_DB_MIGRATOR_USER"), defaultMigratorUser),
				Password: getenv("MEDIA_DB_MIGRATOR_PASSWORD"),
			},
		},
	}
	if cfg.Database.App.Password == "" || cfg.Database.Migrator.Password == "" {
		return Config{}, errors.New("MEDIA_DB_APP_PASSWORD and MEDIA_DB_MIGRATOR_PASSWORD are required")
	}
	return cfg, nil
}

// URLFor returns the database URL with the credentials of the given role.
func (d Database) URLFor(role Credentials) (string, error) {
	u, err := url.Parse(d.URL)
	if err != nil {
		return "", fmt.Errorf("parse database URL: %w", err)
	}
	u.User = url.UserPassword(role.User, role.Password)
	return u.String(), nil
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("MEDIA_PORT must be a port number, got %q", value)
	}
	return port, nil
}

func checkDatabaseURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return errors.New("MEDIA_DB_URL must be a postgres:// URL")
	}
	if u.User != nil {
		return errors.New("MEDIA_DB_URL must not contain credentials; use MEDIA_DB_*_USER and _PASSWORD")
	}
	return nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
