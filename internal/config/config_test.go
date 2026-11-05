package config

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func required() map[string]string {
	return map[string]string{
		"MEDIA_DB_APP_PASSWORD":      "app-secret",
		"MEDIA_DB_MIGRATOR_PASSWORD": "migrator-secret",
	}
}

func TestLoadUsesDefaults(t *testing.T) {
	cfg, err := Load(env(required()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8082 {
		t.Errorf("Port = %d, want 8082", cfg.Port)
	}
	if cfg.Database.URL != defaultDatabaseURL {
		t.Errorf("URL = %q", cfg.Database.URL)
	}
	if cfg.Database.App.User != "media_app" || cfg.Database.Migrator.User != "media_migrator" {
		t.Errorf("users = %q, %q", cfg.Database.App.User, cfg.Database.Migrator.User)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	values := required()
	values["MEDIA_PORT"] = "9090"
	values["MEDIA_DB_URL"] = "postgresql://db:5432/media?sslmode=verify-full"
	values["MEDIA_DB_APP_USER"] = "app"
	values["MEDIA_DB_MIGRATOR_USER"] = "owner"

	cfg, err := Load(env(values))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9090 || cfg.Database.App.User != "app" || cfg.Database.Migrator.User != "owner" {
		t.Errorf("unexpected config %+v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]struct {
		key, value, message string
	}{
		"port not a number":  {"MEDIA_PORT", "http", "MEDIA_PORT"},
		"port out of range":  {"MEDIA_PORT", "70000", "MEDIA_PORT"},
		"not postgres":       {"MEDIA_DB_URL", "mysql://db/media", "MEDIA_DB_URL"},
		"no host":            {"MEDIA_DB_URL", "postgres:///media", "MEDIA_DB_URL"},
		"credentials in url": {"MEDIA_DB_URL", "postgres://u:p@db/media", "credentials"},
		"no app password":    {"MEDIA_DB_APP_PASSWORD", "", "required"},
		"no migrator pass":   {"MEDIA_DB_MIGRATOR_PASSWORD", "", "required"},
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

func TestCredentialsHidePassword(t *testing.T) {
	c := Credentials{User: "media_app", Password: "secret"}
	if s := c.String(); strings.Contains(s, "secret") {
		t.Errorf("String() = %q exposes the password", s)
	}
}
