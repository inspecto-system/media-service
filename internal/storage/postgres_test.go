package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/inspecto-system/media-service/internal/testsupport"
)

func TestToMigrateURL(t *testing.T) {
	cases := map[string]string{
		"postgres://u:p@db:5432/media?sslmode=require": "pgx5://u:p@db:5432/media?sslmode=require",
		"postgresql://db/media":                        "pgx5://db/media",
		"pgx5://db/media":                              "pgx5://db/media",
	}
	for in, want := range cases {
		if got := toMigrateURL(in); got != want {
			t.Errorf("toMigrateURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMigrateIsRepeatableAndAppRoleHasNoDDL(t *testing.T) {
	db := testsupport.StartPostgres(t)
	migratorURL := db.URL(t, db.Config.Migrator)

	if err := Migrate(migratorURL); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := Migrate(migratorURL); err != nil {
		t.Fatalf("second Migrate must be a no-op: %v", err)
	}

	ctx := context.Background()
	pool, err := Connect(ctx, db.URL(t, db.Config.App))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()
	var version int
	if err := pool.QueryRow(ctx, "select version from schema_migrations").Scan(&version); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}
	_, err = pool.Exec(ctx, "create table intruder (id int)")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("app role must not run DDL, got %v", err)
	}
}

func TestConnectFailsForUnreachableDatabase(t *testing.T) {
	_, err := Connect(context.Background(), "postgres://u:p@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err == nil || !strings.Contains(err.Error(), "connect to database") {
		t.Errorf("err = %v", err)
	}
}

func TestConnectRejectsMalformedURL(t *testing.T) {
	_, err := Connect(context.Background(), "postgres://db:notaport/media")
	if err == nil || !strings.Contains(err.Error(), "configure database pool") {
		t.Errorf("err = %v", err)
	}
}

func TestMigrateFailsForUnreachableDatabase(t *testing.T) {
	err := Migrate("postgres://u:p@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if err == nil || !strings.Contains(err.Error(), "open database for migrations") {
		t.Errorf("err = %v", err)
	}
}
