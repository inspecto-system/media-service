package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/inspecto-system/media-service/internal/config"
	"github.com/inspecto-system/media-service/internal/testsupport"
)

func TestPutGetDelete(t *testing.T) {
	store := New(testsupport.StartSeaweedFS(t))
	ctx := context.Background()
	data := []byte("\xFF\xD8\xFFjpeg bytes")

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := store.Put(ctx, "p/1/original.jpg", data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	body, size, err := store.Get(ctx, "p/1/original.jpg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || !bytes.Equal(got, data) || size != int64(len(data)) {
		t.Fatalf("Get = %q (%d), %v", got, size, err)
	}

	if err := store.Delete(ctx, "p/1/original.jpg", "p/1/never-stored.jpg"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store.Get(ctx, "p/1/original.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: err = %v, want ErrNotFound", err)
	}
}

func TestUnreachableStorageFails(t *testing.T) {
	store := New(config.Storage{Endpoint: "http://127.0.0.1:1", Region: "us-east-1", Bucket: "photos", AccessKey: "a", SecretKey: "b"})
	ctx := context.Background()

	if err := store.Ping(ctx); err == nil {
		t.Error("Ping: want an error")
	}
	if err := store.Put(ctx, "k", []byte("x")); err == nil {
		t.Error("Put: want an error")
	}
	if _, _, err := store.Get(ctx, "k"); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Get: err = %v, want a storage error", err)
	}
	if err := store.Delete(ctx, "k"); err == nil {
		t.Error("Delete: want an error")
	}
}
