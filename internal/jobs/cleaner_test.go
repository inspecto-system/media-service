package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/objectstore"
	"github.com/inspecto-system/media-service/internal/storage"
	"github.com/inspecto-system/media-service/internal/testsupport"
)

func repository(t *testing.T) *storage.Photos {
	t.Helper()
	db := testsupport.StartPostgres(t)
	if err := storage.Migrate(db.URL(t, db.Config.Migrator)); err != nil {
		t.Fatal(err)
	}
	pool, err := storage.Connect(context.Background(), db.URL(t, db.Config.App))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return storage.NewPhotos(pool)
}

func photo(created time.Time, status string) storage.Photo {
	id := uuid.New()
	prefix := "p/" + id.String() + "/"
	p := storage.Photo{
		ID: id, OwnerID: uuid.New(), PropertyID: uuid.New(), Status: status,
		OriginalKey: prefix + "original.jpg", PreviewKey: prefix + "preview.jpg", ThumbKey: prefix + "thumb.jpg",
		ContentType: "image/jpeg", SizeBytes: 3, Width: 1, Height: 1,
		SHA256: strings.Repeat("b", 64), CreatedAt: created.UTC().Truncate(time.Microsecond),
	}
	if status == storage.StatusAttached {
		at := p.CreatedAt.Add(time.Minute)
		p.AttachedAt = &at
	}
	return p
}

// seed stores photos with their files.
func seed(t *testing.T, repo *storage.Photos, files *objectstore.Store, photos ...storage.Photo) {
	t.Helper()
	ctx := context.Background()
	for _, p := range photos {
		if err := repo.Insert(ctx, p); err != nil {
			t.Fatal(err)
		}
		for _, key := range p.Keys() {
			if err := files.Put(ctx, key, []byte("jpg")); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCleanerRemovesExpiredUnattachedPhotosWithFiles(t *testing.T) {
	repo := repository(t)
	files := objectstore.New(testsupport.StartSeaweedFS(t))
	ctx := context.Background()
	now := time.Now()
	expired := photo(now.Add(-25*time.Hour), storage.StatusUploaded)
	fresh := photo(now.Add(-time.Hour), storage.StatusUploaded)
	attached := photo(now.Add(-72*time.Hour), storage.StatusAttached)
	seed(t, repo, files, expired, fresh, attached)
	var logs bytes.Buffer
	cleaner := NewCleaner(repo, files, 24*time.Hour, time.Hour, slog.New(slog.NewJSONHandler(&logs, nil)))

	if removed := cleaner.clean(ctx); removed != 1 {
		t.Fatalf("removed %d, want 1", removed)
	}

	if _, err := repo.Get(ctx, expired.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expired photo kept: %v", err)
	}
	for _, key := range expired.Keys() {
		if _, _, err := files.Get(ctx, key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("file %s kept: %v", key, err)
		}
	}
	for _, p := range []storage.Photo{fresh, attached} {
		if _, err := repo.Get(ctx, p.ID); err != nil {
			t.Errorf("photo %s removed: %v", p.ID, err)
		}
	}
	if !strings.Contains(logs.String(), "unattached photos deleted") {
		t.Errorf("no summary in logs: %s", logs.String())
	}
}

// fakeExpired returns prepared batches, then nothing.
type fakeExpired struct {
	mu      sync.Mutex
	batches [][]storage.Photo
	err     error
	calls   int
}

func (f *fakeExpired) TakeExpired(context.Context, time.Time, int) ([]storage.Photo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	return batch, nil
}

type failingFiles struct{}

func (failingFiles) Delete(context.Context, ...string) error { return errors.New("storage down") }

func TestCleanerTakesFullBatchesUntilEmpty(t *testing.T) {
	full := make([]storage.Photo, batchSize)
	for i := range full {
		full[i] = photo(time.Now(), storage.StatusUploaded)
	}
	expired := &fakeExpired{batches: [][]storage.Photo{full, {photo(time.Now(), storage.StatusUploaded)}}}
	var logs bytes.Buffer
	cleaner := NewCleaner(expired, failingFiles{}, time.Hour, time.Hour, slog.New(slog.NewJSONHandler(&logs, nil)))

	removed := cleaner.clean(context.Background())

	if removed != batchSize+1 || expired.calls != 2 {
		t.Errorf("removed %d in %d calls", removed, expired.calls)
	}
	if !strings.Contains(logs.String(), "photo files not deleted") {
		t.Errorf("failed file deletion not logged: %s", logs.String())
	}
}

func TestCleanerLogsRepositoryErrors(t *testing.T) {
	var logs bytes.Buffer
	cleaner := NewCleaner(&fakeExpired{err: errors.New("db down")}, failingFiles{}, time.Hour, time.Hour,
		slog.New(slog.NewJSONHandler(&logs, nil)))

	if removed := cleaner.clean(context.Background()); removed != 0 {
		t.Errorf("removed %d", removed)
	}
	if !strings.Contains(logs.String(), "db down") {
		t.Errorf("error not logged: %s", logs.String())
	}
}

func TestRunCleansPeriodicallyUntilCancelled(t *testing.T) {
	expired := &fakeExpired{}
	cleaner := NewCleaner(expired, failingFiles{}, time.Hour, 10*time.Millisecond,
		slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() { cleaner.Run(ctx); close(done) }()
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
	expired.mu.Lock()
	defer expired.mu.Unlock()
	if expired.calls < 2 {
		t.Errorf("cleaned %d times, want several", expired.calls)
	}
}
