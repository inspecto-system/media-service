package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspecto-system/media-service/internal/config"
	"github.com/inspecto-system/media-service/internal/testsupport"
)

func listen(t *testing.T) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener
}

func get(t *testing.T, url string) (int, map[string]string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, body
}

func TestRunServesHealthAndStopsGracefully(t *testing.T) {
	db := testsupport.StartPostgres(t)
	listener := listen(t)
	base := "http://" + listener.Addr().String()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- Run(ctx, config.Config{Database: db.Config}, logger, listener) }()

	deadline := time.Now().Add(30 * time.Second)
	status := 0
	var body map[string]string
	for status != http.StatusOK && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		status, body = get(t, base+"/health/ready")
	}
	if status != http.StatusOK || body["status"] != "UP" {
		t.Fatalf("ready = %d %v; logs: %s", status, body, logs.String())
	}
	if status, _ := get(t, base+"/health/live"); status != http.StatusOK {
		t.Errorf("live = %d", status)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if !strings.Contains(logs.String(), "media-service stopped") {
		t.Errorf("no stop record in logs: %s", logs.String())
	}
}

func TestRunFailsWithoutDatabaseAndReleasesListener(t *testing.T) {
	listener := listen(t)
	cfg := config.Config{Database: config.Database{
		URL:      "postgres://127.0.0.1:1/inspecto_media?sslmode=disable&connect_timeout=1",
		App:      config.Credentials{User: "media_app", Password: "x"},
		Migrator: config.Credentials{User: "media_migrator", Password: "x"},
	}}

	err := Run(context.Background(), cfg, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), listener)

	if err == nil || !strings.Contains(err.Error(), "migrations") {
		t.Fatalf("err = %v", err)
	}
	if _, acceptErr := listener.Accept(); !errors.Is(acceptErr, net.ErrClosed) {
		t.Errorf("listener still open: %v", acceptErr)
	}
}
