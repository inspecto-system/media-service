package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

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

func send(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, body
}

func newRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// fakeCore answers service token requests and allows the owner on the property.
func fakeCore(t *testing.T, owner, property uuid.UUID) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/auth/service-token" {
			_, _ = w.Write([]byte(`{"access_token":"service","token_type":"Bearer","expires_in":900}`))
			return
		}
		if r.URL.Path == "/internal/v1/access/properties/"+property.String() && r.URL.Query().Get("userId") == owner.String() {
			_, _ = w.Write([]byte(`{"allowed":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func uploadBody(t *testing.T, property uuid.UUID) (*bytes.Buffer, string) {
	t.Helper()
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 400, 300))); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("propertyId", property.String())
	part, err := w.CreateFormFile("file", "room.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(img.Bytes())
	_ = w.Close()
	return &body, w.FormDataContentType()
}

func TestRunServesPhotosAndStopsGracefully(t *testing.T) {
	owner, property := uuid.New(), uuid.New()
	issuer := testsupport.NewTokenIssuer(t)
	cfg := config.Config{
		Database: testsupport.StartPostgres(t).Config,
		Auth:     config.Auth{JWKSURL: issuer.JWKSURL, Issuer: "inspecto-core", Audience: "inspecto-api"},
		Core:     config.Core{URL: fakeCore(t, owner, property), ClientID: "media-service", ClientSecret: "secret"},
		Storage:  testsupport.StartSeaweedFS(t),
		Photos: config.Photos{
			MaxFileSizeBytes: 1 << 20, MaxPixels: 1_000_000, MaxPhotosPerObservation: 10,
			Workers: 1, UnattachedTTL: time.Hour,
		},
	}
	listener := listen(t)
	base := "http://" + listener.Addr().String()
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- Run(ctx, cfg, slog.New(slog.NewJSONHandler(&logs, nil)), listener) }()

	waitReady(t, base, &logs)

	token := "Bearer " + issuer.Token(t, owner, "INSPECTOR")
	photoID := uploadPhoto(t, base, token, property)
	checkThumb(t, base, token, photoID)
	stranger := newRequest(t, http.MethodGet, base+"/api/v1/photos/"+photoID+"/content", nil)
	stranger.Header.Set("Authorization", "Bearer "+issuer.Token(t, uuid.New(), "INSPECTOR"))
	if status, _ := send(t, stranger); status != http.StatusNotFound {
		t.Errorf("stranger got %d, want 404", status)
	}

	cancel()
	waitStopped(t, done, &logs)
}

func waitReady(t *testing.T, base string, logs *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if status, _ := send(t, newRequest(t, http.MethodGet, base+"/health/ready", nil)); status == http.StatusOK {
			return
		}
	}
	t.Fatalf("not ready; logs: %s", logs.String())
}

func uploadPhoto(t *testing.T, base, token string, property uuid.UUID) string {
	t.Helper()
	body, contentType := uploadBody(t, property)
	upload := newRequest(t, http.MethodPost, base+"/api/v1/photos", body)
	upload.Header.Set("Authorization", token)
	upload.Header.Set("Content-Type", contentType)
	status, created := send(t, upload)
	var photo struct {
		ID          string `json:"id"`
		ContentType string `json:"contentType"`
	}
	if err := json.Unmarshal(created, &photo); status != http.StatusCreated || err != nil || photo.ContentType != "image/jpeg" {
		t.Fatalf("upload: %d %s", status, created)
	}
	return photo.ID
}

func checkThumb(t *testing.T, base, token, photoID string) {
	t.Helper()
	download := newRequest(t, http.MethodGet, base+"/api/v1/photos/"+photoID+"/content?variant=thumb", nil)
	download.Header.Set("Authorization", token)
	status, jpegData := send(t, download)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(jpegData))
	if status != http.StatusOK || err != nil || format != "jpeg" || cfg.Width != 320 {
		t.Fatalf("download: %d %s %v %v", status, format, cfg, err)
	}
}

func waitStopped(t *testing.T, done <-chan error, logs *bytes.Buffer) {
	t.Helper()
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
