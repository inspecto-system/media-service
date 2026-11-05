package config

import (
	"strings"
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func required() map[string]string {
	return map[string]string{
		"MEDIA_DB_APP_PASSWORD":      "app-secret",
		"MEDIA_DB_MIGRATOR_PASSWORD": "migrator-secret",
		"MEDIA_CORE_CLIENT_SECRET":   "client-secret",
		"MEDIA_S3_ACCESS_KEY":        "media",
		"MEDIA_S3_SECRET_KEY":        "s3-secret",
	}
}

func TestLoadUsesDefaults(t *testing.T) {
	cfg, err := Load(env(required()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := Config{
		Port: 8082,
		Database: Database{
			URL:      "postgres://127.0.0.1:5432/inspecto_media?sslmode=require",
			App:      Credentials{User: "media_app", Password: "app-secret"},
			Migrator: Credentials{User: "media_migrator", Password: "migrator-secret"},
		},
		Auth: Auth{
			JWKSURL: "http://127.0.0.1:8081/api/v1/.well-known/jwks.json", Issuer: "inspecto-core", Audience: "inspecto-api",
		},
		Core: Core{URL: "http://127.0.0.1:8081", ClientID: "media-service", ClientSecret: "client-secret"},
		Storage: Storage{
			Endpoint: "http://127.0.0.1:8333", Region: "us-east-1", Bucket: "photos", AccessKey: "media", SecretKey: "s3-secret",
		},
		Photos: Photos{
			MaxFileSizeBytes: 15 << 20, MaxPixels: 50_000_000, MaxPhotosPerObservation: 10,
			Workers: 2, UnattachedTTL: 24 * time.Hour,
		},
	}
	if cfg != want {
		t.Errorf("Load = %+v, want %+v", cfg, want)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	values := required()
	values["MEDIA_PORT"] = "9090"
	values["MEDIA_DB_URL"] = "postgresql://db:5432/media?sslmode=verify-full"
	values["MEDIA_S3_ENDPOINT"] = "https://s3.internal:8333"
	values["MEDIA_MAX_PIXELS"] = "1000"
	values["MEDIA_UNATTACHED_TTL"] = "90m"

	cfg, err := Load(env(values))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9090 || cfg.Photos.MaxPixels != 1000 || cfg.Photos.UnattachedTTL != 90*time.Minute ||
		cfg.Storage.Endpoint != "https://s3.internal:8333" {
		t.Errorf("unexpected config %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]struct {
		key, value, message string
	}{
		"port not a number":    {"MEDIA_PORT", "http", "MEDIA_PORT"},
		"port out of range":    {"MEDIA_PORT", "70000", "MEDIA_PORT"},
		"not postgres":         {"MEDIA_DB_URL", "mysql://db/media", "MEDIA_DB_URL"},
		"no host":              {"MEDIA_DB_URL", "postgres:///media", "MEDIA_DB_URL"},
		"credentials in url":   {"MEDIA_DB_URL", "postgres://u:p@db/media", "credentials"},
		"jwks not http":        {"MEDIA_JWKS_URL", "file:///etc/passwd", "MEDIA_JWKS_URL"},
		"bad duration":         {"MEDIA_UNATTACHED_TTL", "tomorrow", "MEDIA_UNATTACHED_TTL"},
		"negative duration":    {"MEDIA_UNATTACHED_TTL", "-1h", "MEDIA_UNATTACHED_TTL"},
		"no app password":      {"MEDIA_DB_APP_PASSWORD", "", "MEDIA_DB_APP_PASSWORD is required"},
		"no client secret":     {"MEDIA_CORE_CLIENT_SECRET", "", "MEDIA_CORE_CLIENT_SECRET is required"},
		"no s3 secret":         {"MEDIA_S3_SECRET_KEY", "", "MEDIA_S3_SECRET_KEY is required"},
		"workers out of range": {"MEDIA_IMAGE_WORKERS", "0", "MEDIA_IMAGE_WORKERS"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			values := required()
			values[tc.key] = tc.value
			_, err := Load(env(values))
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.message)
			}
		})
	}
}

func TestLoadReportsAllMissingSecrets(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil {
		t.Fatal("want an error")
	}
	for key := range required() {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("%s not reported in %v", key, err)
		}
	}
}

func TestURLForAddsEscapedCredentials(t *testing.T) {
	db := Database{URL: "postgres://127.0.0.1:55432/inspecto_media?sslmode=require"}

	got, err := db.URLFor(Credentials{User: "media_app", Password: "p@ss/word"})
	if err != nil {
		t.Fatalf("URLFor: %v", err)
	}

	want := "postgres://media_app:p%40ss%2Fword@127.0.0.1:55432/inspecto_media?sslmode=require"
	if got != want {
		t.Errorf("URLFor = %q, want %q", got, want)
	}
}

func TestURLForRejectsMalformedURL(t *testing.T) {
	if _, err := (Database{URL: "postgres://db:port/x"}).URLFor(Credentials{}); err == nil {
		t.Error("want an error")
	}
}

func TestCredentialsHidePassword(t *testing.T) {
	c := Credentials{User: "media_app", Password: "secret"}
	if s := c.String(); strings.Contains(s, "secret") {
		t.Errorf("String() = %q exposes the password", s)
	}
}
