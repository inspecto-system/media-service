package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/config"
	"github.com/inspecto-system/media-service/internal/core"
	"github.com/inspecto-system/media-service/internal/imaging"
	"github.com/inspecto-system/media-service/internal/objectstore"
	"github.com/inspecto-system/media-service/internal/storage"
	"github.com/inspecto-system/media-service/internal/testsupport"
)

// fakeAccess stands for core-service: an external boundary (CODE_QUALITY.md §4).
type fakeAccess struct {
	mu      sync.Mutex
	allowed map[uuid.UUID]uuid.UUID // property -> owner
	err     error
	asked   []core.Action
}

func (f *fakeAccess) CanAccess(_ context.Context, propertyID, userID uuid.UUID, action core.Action) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, action)
	if f.err != nil {
		return false, f.err
	}
	return f.allowed[propertyID] == userID, nil
}

type fixture struct {
	photos   *Photos
	repo     *storage.Photos
	files    *objectstore.Store
	access   *fakeAccess
	owner    uuid.UUID
	property uuid.UUID
}

// Both containers are started once for the package (TestMain): they are slow to start, and every
// test uses fresh ids, so tests do not see each other's data.
var (
	sharedDB config.Database
	sharedS3 config.Storage
)

func TestMain(m *testing.M) {
	os.Exit(runWithContainers(m))
}

func runWithContainers(m *testing.M) int {
	ctx := context.Background()
	db, stopDB, err := testsupport.RunPostgres(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer stopDB()
	s3, stopS3, err := testsupport.RunSeaweedFS(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer stopS3()
	migrator, err := db.Config.URLFor(db.Config.Migrator)
	if err == nil {
		err = storage.Migrate(migrator)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	sharedDB, sharedS3 = db.Config, s3
	return m.Run()
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	appURL, err := sharedDB.URLFor(sharedDB.App)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := storage.Connect(context.Background(), appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := fixture{
		repo:     storage.NewPhotos(pool),
		files:    objectstore.New(sharedS3),
		access:   &fakeAccess{allowed: map[uuid.UUID]uuid.UUID{}},
		owner:    uuid.New(),
		property: uuid.New(),
	}
	f.access.allowed[f.property] = f.owner
	f.photos = NewPhotos(f.repo, f.files, f.access, Settings{
		Limits:  imaging.Limits{MaxPixels: 1_000_000},
		Workers: 2,
		Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	return f
}

func pngPhoto(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(0, 0, color.Black)
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func readAll(t *testing.T, body io.ReadCloser) []byte {
	t.Helper()
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func uploadWide(t *testing.T, f fixture) storage.Photo {
	t.Helper()
	photo, err := f.photos.Upload(context.Background(), f.owner, Upload{PropertyID: f.property, Data: pngPhoto(t, 1600, 600)})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	return photo
}

// download returns the bytes of a variant and checks the reported length.
func download(t *testing.T, f fixture, photoID uuid.UUID, variant string) []byte {
	t.Helper()
	body, length, err := f.photos.Content(context.Background(), f.owner, photoID, variant)
	if err != nil {
		t.Fatalf("Content(%s): %v", variant, err)
	}
	data := readAll(t, body)
	if length != int64(len(data)) {
		t.Errorf("%s: length %d, read %d bytes", variant, length, len(data))
	}
	return data
}

func TestUploadStoresMetadata(t *testing.T) {
	f := newFixture(t)

	photo := uploadWide(t, f)

	want := storage.Photo{OwnerID: f.owner, PropertyID: f.property, Status: storage.StatusUploaded,
		ContentType: "image/jpeg", Width: 1600, Height: 600}
	got := storage.Photo{OwnerID: photo.OwnerID, PropertyID: photo.PropertyID, Status: photo.Status,
		ContentType: photo.ContentType, Width: photo.Width, Height: photo.Height}
	if got != want {
		t.Errorf("photo = %+v, want %+v", got, want)
	}
	if !strings.HasPrefix(photo.OriginalKey, f.property.String()+"/"+photo.ID.String()+"/") {
		t.Errorf("key %q not built from ids", photo.OriginalKey)
	}
	if stored, err := f.repo.Get(context.Background(), photo.ID); err != nil || stored.SHA256 != photo.SHA256 {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	if f.access.asked[0] != core.Write {
		t.Errorf("access asked %v; want write for upload", f.access.asked)
	}
}

func TestUploadStoresThreeJPEGVariants(t *testing.T) {
	f := newFixture(t)
	photo := uploadWide(t, f)

	sizes := map[string]image.Point{VariantOriginal: {1600, 600}, VariantPreview: {1280, 480}, VariantThumb: {320, 120}}
	for variant, size := range sizes {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(download(t, f, photo.ID, variant)))
		if err != nil || format != "jpeg" || (image.Point{cfg.Width, cfg.Height}) != size {
			t.Errorf("%s: %s %dx%d, %v", variant, format, cfg.Width, cfg.Height, err)
		}
	}
	if f.access.asked[1] != core.Read {
		t.Errorf("access asked %v; want read for content", f.access.asked)
	}
}

func TestOriginalMatchesChecksumAndSize(t *testing.T) {
	f := newFixture(t)
	photo := uploadWide(t, f)

	data := download(t, f, photo.ID, VariantOriginal)

	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != photo.SHA256 || int64(len(data)) != photo.SizeBytes {
		t.Error("checksum or size of the original does not match the metadata")
	}
}

func TestUploadWithSameIdempotencyKeyReturnsFirstPhoto(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	in := Upload{PropertyID: f.property, IdempotencyKey: "upload-" + uuid.NewString()[:8], Data: pngPhoto(t, 50, 50)}

	first, err := f.photos.Upload(ctx, f.owner, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.photos.Upload(ctx, f.owner, in)
	if err != nil || second.ID != first.ID {
		t.Fatalf("second upload = %s, %v; want %s", second.ID, err, first.ID)
	}
}

func TestUploadRefusals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cases := map[string]struct {
		user uuid.UUID
		in   Upload
		want error
	}{
		"property of another user": {uuid.New(), Upload{PropertyID: f.property, Data: pngPhoto(t, 10, 10)}, ErrNotFound},
		"unknown property":         {f.owner, Upload{PropertyID: uuid.New(), Data: pngPhoto(t, 10, 10)}, ErrNotFound},
		"not an image":             {f.owner, Upload{PropertyID: f.property, Data: []byte("<svg onload=alert(1)>")}, imaging.ErrUnsupportedType},
		"too many pixels":          {f.owner, Upload{PropertyID: f.property, Data: pngPhoto(t, 1001, 1000)}, imaging.ErrTooManyPixels},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := f.photos.Upload(ctx, tc.user, tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUploadWhenCoreIsDown(t *testing.T) {
	f := newFixture(t)
	f.access.err = core.ErrUnavailable

	_, err := f.photos.Upload(context.Background(), f.owner, Upload{PropertyID: f.property, Data: pngPhoto(t, 10, 10)})

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestUploadWhenStorageIsDownLeavesNoMetadata(t *testing.T) {
	f := newFixture(t)
	down := objectstore.New(config.Storage{Endpoint: "http://127.0.0.1:1", Region: "us-east-1", Bucket: "photos", AccessKey: "a", SecretKey: "b"})
	photos := NewPhotos(f.repo, down, f.access, Settings{
		Limits: imaging.Limits{MaxPixels: 1_000_000}, Workers: 1, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	key := "down-" + uuid.NewString()[:8]

	_, err := photos.Upload(context.Background(), f.owner, Upload{PropertyID: f.property, IdempotencyKey: key, Data: pngPhoto(t, 10, 10)})

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if _, err := f.repo.FindByIdempotencyKey(context.Background(), f.owner, key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("metadata stored without files: %v", err)
	}
}

func TestUploadWaitsForWorkerUntilCancelled(t *testing.T) {
	f := newFixture(t)
	f.photos.workers <- struct{}{}
	f.photos.workers <- struct{}{} // both workers busy
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := f.photos.Upload(ctx, f.owner, Upload{PropertyID: f.property, Data: pngPhoto(t, 10, 10)})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestContentRefusals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	photo, err := f.photos.Upload(ctx, f.owner, Upload{PropertyID: f.property, Data: pngPhoto(t, 10, 10)})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := f.photos.Content(ctx, uuid.New(), photo.ID, VariantPreview); !errors.Is(err, ErrNotFound) {
		t.Errorf("stranger: err = %v", err)
	}
	if _, _, err := f.photos.Content(ctx, f.owner, uuid.New(), VariantPreview); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown photo: err = %v", err)
	}
	if err := f.files.Delete(ctx, photo.ThumbKey); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.photos.Content(ctx, f.owner, photo.ID, VariantThumb); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file: err = %v", err)
	}
	f.access.err = core.ErrUnavailable
	if _, _, err := f.photos.Content(ctx, f.owner, photo.ID, VariantPreview); !errors.Is(err, ErrUnavailable) {
		t.Errorf("core down: err = %v", err)
	}
}

func TestDeleteRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	photo, err := f.photos.Upload(ctx, f.owner, Upload{PropertyID: f.property, Data: pngPhoto(t, 10, 10)})
	if err != nil {
		t.Fatal(err)
	}
	attached := photo
	attached.ID = uuid.New()
	attached.Status = storage.StatusAttached
	now := time.Now().UTC().Truncate(time.Microsecond)
	attached.AttachedAt = &now
	if err := f.repo.Insert(ctx, attached); err != nil {
		t.Fatal(err)
	}

	if err := f.photos.Delete(ctx, uuid.New(), photo.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("stranger: err = %v", err)
	}
	if err := f.photos.Delete(ctx, f.owner, attached.ID); !errors.Is(err, ErrAttached) {
		t.Errorf("attached: err = %v", err)
	}
	if err := f.photos.Delete(ctx, f.owner, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: err = %v", err)
	}
	if err := f.photos.Delete(ctx, f.owner, photo.ID); err != nil {
		t.Fatalf("owner: %v", err)
	}
	if _, _, err := f.files.Get(ctx, photo.OriginalKey); !errors.Is(err, objectstore.ErrNotFound) {
		t.Errorf("file kept after delete: %v", err)
	}
}
