package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/config"
	"github.com/inspecto-system/media-service/internal/objectstore"
	"github.com/inspecto-system/media-service/internal/storage"
)

func uploaded(t *testing.T, f fixture, owner, property uuid.UUID) storage.Photo {
	t.Helper()
	f.access.mu.Lock()
	f.access.allowed[property] = owner
	f.access.mu.Unlock()
	photo, err := f.photos.Upload(context.Background(), owner, Upload{PropertyID: property, Data: pngPhoto(t, 20, 10)})
	if err != nil {
		t.Fatal(err)
	}
	return photo
}

func assertGone(t *testing.T, f fixture, photo storage.Photo) {
	t.Helper()
	if _, err := f.repo.Get(context.Background(), photo.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("row of %s kept: %v", photo.ID, err)
	}
	for _, key := range photo.Keys() {
		if _, _, err := f.files.Get(context.Background(), key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("file %s kept: %v", key, err)
		}
	}
}

func TestAttachIsIdempotentAndKeepsFirstTime(t *testing.T) {
	f := newFixture(t)
	internal := NewInternal(f.repo, f.files)
	ctx := context.Background()
	photo := uploaded(t, f, f.owner, f.property)

	if err := internal.Attach(ctx, photo.ID); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	first, err := internal.Metadata(ctx, photo.ID)
	if err != nil || first.Status != storage.StatusAttached || first.AttachedAt == nil {
		t.Fatalf("after attach: %+v, %v", first, err)
	}
	if err := internal.Attach(ctx, photo.ID); err != nil {
		t.Fatalf("repeated Attach: %v", err)
	}
	second, _ := internal.Metadata(ctx, photo.ID)
	if !second.AttachedAt.Equal(*first.AttachedAt) {
		t.Errorf("attachment time changed: %v -> %v", first.AttachedAt, second.AttachedAt)
	}
	if err := f.photos.Delete(ctx, f.owner, photo.ID); !errors.Is(err, ErrAttached) {
		t.Errorf("user deleted an attached photo: %v", err)
	}
}

func TestInternalLookupsOfMissingPhotos(t *testing.T) {
	f := newFixture(t)
	internal := NewInternal(f.repo, f.files)
	ctx := context.Background()

	if err := internal.Attach(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Attach: %v", err)
	}
	if _, err := internal.Metadata(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Metadata: %v", err)
	}
	if _, _, err := internal.Content(ctx, uuid.New(), VariantPreview); !errors.Is(err, ErrNotFound) {
		t.Errorf("Content: %v", err)
	}
	if err := internal.Delete(ctx, uuid.New()); err != nil {
		t.Errorf("Delete of a missing photo must succeed: %v", err)
	}
}

func TestInternalContentReturnsAnyPhoto(t *testing.T) {
	f := newFixture(t)
	internal := NewInternal(f.repo, f.files)
	photo := uploaded(t, f, f.owner, f.property)

	body, size, err := internal.Content(context.Background(), photo.ID, VariantThumb)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if data := readAll(t, body); int64(len(data)) != size || size == 0 {
		t.Errorf("read %d bytes, size %d", len(data), size)
	}

	if err := f.files.Delete(context.Background(), photo.ThumbKey); err != nil {
		t.Fatal(err)
	}
	if _, _, err := internal.Content(context.Background(), photo.ID, VariantThumb); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
}

func TestInternalDeleteRemovesAttachedPhotoWithFiles(t *testing.T) {
	f := newFixture(t)
	internal := NewInternal(f.repo, f.files)
	photo := uploaded(t, f, f.owner, f.property)
	if err := internal.Attach(context.Background(), photo.ID); err != nil {
		t.Fatal(err)
	}

	if err := internal.Delete(context.Background(), photo.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	assertGone(t, f, photo)
}

func TestDeletePropertyAndOwnerRemoveOnlyTheirPhotos(t *testing.T) {
	f := newFixture(t)
	internal := NewInternal(f.repo, f.files)
	ctx := context.Background()
	otherProperty, otherOwner := uuid.New(), uuid.New()
	ofProperty := uploaded(t, f, f.owner, f.property)
	ofOwner := uploaded(t, f, f.owner, otherProperty)
	foreign := uploaded(t, f, otherOwner, uuid.New())

	if err := internal.DeleteProperty(ctx, f.property); err != nil {
		t.Fatalf("DeleteProperty: %v", err)
	}
	assertGone(t, f, ofProperty)
	if _, err := f.repo.Get(ctx, ofOwner.ID); err != nil {
		t.Errorf("photo of another property removed: %v", err)
	}

	if err := internal.DeleteOwner(ctx, f.owner); err != nil {
		t.Fatalf("DeleteOwner: %v", err)
	}
	assertGone(t, f, ofOwner)
	if _, err := f.repo.Get(ctx, foreign.ID); err != nil {
		t.Errorf("photo of another owner removed: %v", err)
	}
	if err := internal.DeleteProperty(ctx, uuid.New()); err != nil {
		t.Errorf("nothing to delete must succeed: %v", err)
	}
}

func TestInternalDeleteKeepsRowsWhenFilesCannotBeDeleted(t *testing.T) {
	f := newFixture(t)
	photo := uploaded(t, f, f.owner, f.property)
	down := objectstore.New(config.Storage{Endpoint: "http://127.0.0.1:1", Region: "us-east-1", Bucket: "photos", AccessKey: "a", SecretKey: "b"})
	internal := NewInternal(f.repo, down)

	if err := internal.Delete(context.Background(), photo.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if _, err := f.repo.Get(context.Background(), photo.ID); err != nil {
		t.Errorf("row deleted although files were kept, a retry would lose them: %v", err)
	}
	if _, _, err := internal.Content(context.Background(), photo.ID, VariantPreview); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Content with storage down: %v", err)
	}
}

func TestInternalReportsDatabaseErrors(t *testing.T) {
	f := newFixture(t)
	closed, err := storage.Connect(context.Background(), mustURL(t))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	internal := NewInternal(storage.NewPhotos(closed), f.files)
	ctx := context.Background()

	checks := map[string]error{
		"Metadata":       func() error { _, err := internal.Metadata(ctx, uuid.New()); return err }(),
		"Attach":         internal.Attach(ctx, uuid.New()),
		"Delete":         internal.Delete(ctx, uuid.New()),
		"DeleteProperty": internal.DeleteProperty(ctx, uuid.New()),
		"DeleteOwner":    internal.DeleteOwner(ctx, uuid.New()),
	}
	for name, err := range checks {
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", name, err)
		}
	}
}

func mustURL(t *testing.T) string {
	t.Helper()
	url, err := sharedDB.URLFor(sharedDB.App)
	if err != nil {
		t.Fatal(err)
	}
	return url
}
