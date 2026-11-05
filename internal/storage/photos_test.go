package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspecto-system/media-service/internal/testsupport"
)

func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := testsupport.StartPostgres(t)
	if err := Migrate(db.URL(t, db.Config.Migrator)); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := Connect(context.Background(), db.URL(t, db.Config.App))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newPhoto(owner uuid.UUID, created time.Time, key *string) Photo {
	id := uuid.New()
	prefix := "p/" + id.String() + "/"
	return Photo{
		ID: id, OwnerID: owner, PropertyID: uuid.New(), Status: StatusUploaded,
		OriginalKey: prefix + "original.jpg", PreviewKey: prefix + "preview.jpg", ThumbKey: prefix + "thumb.jpg",
		ContentType: "image/jpeg", SizeBytes: 1024, Width: 40, Height: 30,
		SHA256: strings.Repeat("a", 64), IdempotencyKey: key, CreatedAt: created.UTC().Truncate(time.Microsecond),
	}
}

func TestPhotosRoundTrip(t *testing.T) {
	repo := NewPhotos(migratedPool(t))
	ctx := context.Background()
	key := "upload-0001"
	photo := newPhoto(uuid.New(), time.Now(), &key)

	if err := repo.Insert(ctx, photo); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := repo.Get(ctx, photo.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.IdempotencyKey == nil || *got.IdempotencyKey != key {
		t.Errorf("idempotency key = %v", got.IdempotencyKey)
	}
	got.IdempotencyKey, photo.IdempotencyKey = nil, nil
	if !got.CreatedAt.Equal(photo.CreatedAt) {
		t.Errorf("createdAt = %v, want %v", got.CreatedAt, photo.CreatedAt)
	}
	got.CreatedAt = photo.CreatedAt
	if got != photo || len(got.Keys()) != 3 {
		t.Errorf("Get = %+v, want %+v", got, photo)
	}
}

func TestIdempotencyKeyIsUniquePerOwner(t *testing.T) {
	repo := NewPhotos(migratedPool(t))
	ctx := context.Background()
	owner := uuid.New()
	key := "upload-0002"
	photo := newPhoto(owner, time.Now(), &key)
	if err := repo.Insert(ctx, photo); err != nil {
		t.Fatal(err)
	}

	if found, err := repo.FindByIdempotencyKey(ctx, owner, key); err != nil || found.ID != photo.ID {
		t.Errorf("FindByIdempotencyKey = %v, %v", found.ID, err)
	}
	if _, err := repo.FindByIdempotencyKey(ctx, uuid.New(), key); !errors.Is(err, ErrNotFound) {
		t.Errorf("key of another owner: err = %v", err)
	}
	if err := repo.Insert(ctx, newPhoto(owner, time.Now(), &key)); !errors.Is(err, ErrDuplicateIdempotencyKey) {
		t.Errorf("same key twice: err = %v", err)
	}
	if err := repo.Insert(ctx, newPhoto(uuid.New(), time.Now(), &key)); err != nil {
		t.Errorf("same key of another owner: %v", err)
	}
}

func TestDeleteUploadedOnlyForOwner(t *testing.T) {
	repo := NewPhotos(migratedPool(t))
	ctx := context.Background()
	owner := uuid.New()
	photo := newPhoto(owner, time.Now(), nil)
	if err := repo.Insert(ctx, photo); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.DeleteUploaded(ctx, photo.ID, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stranger: err = %v", err)
	}
	deleted, err := repo.DeleteUploaded(ctx, photo.ID, owner)
	if err != nil || deleted.ID != photo.ID {
		t.Fatalf("owner: %v, %v", deleted.ID, err)
	}
	if _, err := repo.Get(ctx, photo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: err = %v", err)
	}
}

func TestTakeExpiredLeavesFreshAndAttachedPhotos(t *testing.T) {
	pool := migratedPool(t)
	repo := NewPhotos(pool)
	ctx := context.Background()
	now := time.Now()
	old := newPhoto(uuid.New(), now.Add(-25*time.Hour), nil)
	fresh := newPhoto(uuid.New(), now.Add(-time.Hour), nil)
	attached := newPhoto(uuid.New(), now.Add(-48*time.Hour), nil)
	attached.Status = StatusAttached
	attachedAt := now.Add(-47 * time.Hour)
	attached.AttachedAt = &attachedAt
	for _, p := range []Photo{old, fresh, attached} {
		if err := repo.Insert(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	taken, err := repo.TakeExpired(ctx, now.Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatalf("TakeExpired: %v", err)
	}
	if len(taken) != 1 || taken[0].ID != old.ID {
		t.Fatalf("taken = %v", taken)
	}
	for _, p := range []Photo{fresh, attached} {
		if _, err := repo.Get(ctx, p.ID); err != nil {
			t.Errorf("photo %s removed: %v", p.ID, err)
		}
	}
}

func TestRepositoryReportsDatabaseErrors(t *testing.T) {
	pool := migratedPool(t)
	repo := NewPhotos(pool)
	pool.Close()
	ctx := context.Background()

	if err := repo.Insert(ctx, newPhoto(uuid.New(), time.Now(), nil)); err == nil {
		t.Error("Insert: want an error")
	}
	if _, err := repo.Get(ctx, uuid.New()); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Get: err = %v", err)
	}
	if _, err := repo.TakeExpired(ctx, time.Now(), 1); err == nil {
		t.Error("TakeExpired: want an error")
	}
}
