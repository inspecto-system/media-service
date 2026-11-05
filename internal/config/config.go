// Package config reads the media-service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Config is the complete runtime configuration of the service.
type Config struct {
	// Port is the HTTP port the service listens on.
	Port     int
	Database Database
	Auth     Auth
	Core     Core
	Storage  Storage
	Photos   Photos
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

// Auth describes how user access tokens issued by core-service are verified (ADR-007).
type Auth struct {
	JWKSURL  string
	Issuer   string
	Audience string
}

// Core is the internal API of core-service used for access checks.
type Core struct {
	URL          string
	ClientID     string
	ClientSecret string
}

// Storage is the S3-compatible object storage holding the photo files (ADR-024).
type Storage struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
}

// Photos holds the upload limits and processing settings.
type Photos struct {
	MaxFileSizeBytes        int64
	MaxPixels               int64
	MaxPhotosPerObservation int
	// Workers limits concurrent decoding: a decoded 50-megapixel image takes about 200 MB.
	Workers int
	// UnattachedTTL is how long a photo may stay unattached before it is deleted (ADR-018).
	UnattachedTTL time.Duration
}

// Load builds the configuration from the given lookup function (os.Getenv in production) and
// reports every invalid or missing variable at once.
func Load(getenv func(string) string) (Config, error) {
	r := reader{getenv: getenv}
	cfg := Config{
		Port: int(r.integer("MEDIA_PORT", 8082, 1, 65535)),
		Database: Database{
			URL:      r.databaseURL("MEDIA_DB_URL", "postgres://127.0.0.1:5432/inspecto_media?sslmode=require"),
			App:      Credentials{User: r.text("MEDIA_DB_APP_USER", "media_app"), Password: r.secret("MEDIA_DB_APP_PASSWORD")},
			Migrator: Credentials{User: r.text("MEDIA_DB_MIGRATOR_USER", "media_migrator"), Password: r.secret("MEDIA_DB_MIGRATOR_PASSWORD")},
		},
		Auth: Auth{
			JWKSURL:  r.httpURL("MEDIA_JWKS_URL", "http://127.0.0.1:8081/api/v1/.well-known/jwks.json"),
			Issuer:   r.text("MEDIA_JWT_ISSUER", "inspecto-core"),
			Audience: r.text("MEDIA_JWT_AUDIENCE", "inspecto-api"),
		},
		Core: Core{
			URL:          r.httpURL("MEDIA_CORE_URL", "http://127.0.0.1:8081"),
			ClientID:     "media-service",
			ClientSecret: r.secret("MEDIA_CORE_CLIENT_SECRET"),
		},
		Storage: Storage{
			Endpoint:  r.httpURL("MEDIA_S3_ENDPOINT", "http://127.0.0.1:8333"),
			Region:    r.text("MEDIA_S3_REGION", "us-east-1"),
			Bucket:    r.text("MEDIA_S3_BUCKET", "photos"),
			AccessKey: r.secret("MEDIA_S3_ACCESS_KEY"),
			SecretKey: r.secret("MEDIA_S3_SECRET_KEY"),
		},
		Photos: Photos{
			MaxFileSizeBytes:        r.integer("MEDIA_MAX_FILE_SIZE_BYTES", 15<<20, 1<<10, 100<<20),
			MaxPixels:               r.integer("MEDIA_MAX_PIXELS", 50_000_000, 1, 200_000_000),
			MaxPhotosPerObservation: int(r.integer("MEDIA_MAX_PHOTOS_PER_OBSERVATION", 10, 1, 100)),
			Workers:                 int(r.integer("MEDIA_IMAGE_WORKERS", 2, 1, 64)),
			UnattachedTTL:           r.duration("MEDIA_UNATTACHED_TTL", 24*time.Hour),
		},
	}
	if err := errors.Join(r.errs...); err != nil {
		return Config{}, err
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

// reader reads variables and collects the problems found.
type reader struct {
	getenv func(string) string
	errs   []error
}

func (r *reader) text(key, fallback string) string {
	if value := r.getenv(key); value != "" {
		return value
	}
	return fallback
}

func (r *reader) secret(key string) string {
	value := r.getenv(key)
	if value == "" {
		r.errs = append(r.errs, fmt.Errorf("%s is required", key))
	}
	return value
}

func (r *reader) integer(key string, fallback, minimum, maximum int64) int64 {
	raw := r.getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum {
		r.errs = append(r.errs, fmt.Errorf("%s must be a number from %d to %d, got %q", key, minimum, maximum, raw))
		return fallback
	}
	return value
}

func (r *reader) duration(key string, fallback time.Duration) time.Duration {
	raw := r.getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		r.errs = append(r.errs, fmt.Errorf("%s must be a positive duration such as 24h, got %q", key, raw))
		return fallback
	}
	return value
}

func (r *reader) databaseURL(key, fallback string) string {
	value := r.text(key, fallback)
	u, err := url.Parse(value)
	switch {
	case err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "":
		r.errs = append(r.errs, fmt.Errorf("%s must be a postgres:// URL", key))
	case u.User != nil:
		r.errs = append(r.errs, fmt.Errorf("%s must not contain credentials; use MEDIA_DB_*_USER and _PASSWORD", key))
	}
	return value
}

func (r *reader) httpURL(key, fallback string) string {
	value := r.text(key, fallback)
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		r.errs = append(r.errs, fmt.Errorf("%s must be an http(s) URL", key))
	}
	return value
}
