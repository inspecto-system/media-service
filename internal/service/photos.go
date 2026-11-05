// Package service holds the business rules of photos: who may upload, read and delete them, and
// how an upload becomes three stored variants (T-205, ADR-025).
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/core"
	"github.com/inspecto-system/media-service/internal/imaging"
	"github.com/inspecto-system/media-service/internal/objectstore"
	"github.com/inspecto-system/media-service/internal/storage"
)

var (
	// ErrNotFound means the photo or property does not exist or the user has no access to it; the
	// two cases are not told apart (ADR-009).
	ErrNotFound = errors.New("not found")
	// ErrAttached means the photo belongs to an observation and cannot be deleted directly.
	ErrAttached = errors.New("photo is attached")
	// ErrUnavailable means a dependency (core-service, storage) could not be reached.
	ErrUnavailable = errors.New("dependency unavailable")
)

// Variant names of the API (common.yaml, PhotoVariant).
const (
	VariantOriginal = "original"
	VariantPreview  = "preview"
	VariantThumb    = "thumb"
)

// Access answers whether a user may act on a property (core-service).
type Access interface {
	CanAccess(ctx context.Context, propertyID, userID uuid.UUID, action core.Action) (bool, error)
}

// Repository stores photo metadata.
type Repository interface {
	Insert(ctx context.Context, p storage.Photo) error
	Get(ctx context.Context, id uuid.UUID) (storage.Photo, error)
	FindByIdempotencyKey(ctx context.Context, ownerID uuid.UUID, key string) (storage.Photo, error)
	DeleteUploaded(ctx context.Context, id, ownerID uuid.UUID) (storage.Photo, error)
}

// Files stores photo files.
type Files interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	Delete(ctx context.Context, keys ...string) error
}

// Photos implements the photo operations.
type Photos struct {
	repo    Repository
	files   Files
	access  Access
	limits  imaging.Limits
	workers chan struct{}
	logger  *slog.Logger
	now     func() time.Time
}

// Settings configure the service.
type Settings struct {
	Limits imaging.Limits
	// Workers is the number of images decoded at the same time.
	Workers int
	Logger  *slog.Logger
}

// NewPhotos returns the service.
func NewPhotos(repo Repository, files Files, access Access, settings Settings) *Photos {
	return &Photos{
		repo:    repo,
		files:   files,
		access:  access,
		limits:  settings.Limits,
		workers: make(chan struct{}, settings.Workers),
		logger:  settings.Logger,
		now:     time.Now,
	}
}

// Upload is a photo sent by a user.
type Upload struct {
	PropertyID uuid.UUID
	// IdempotencyKey is optional; a repeated upload with the same key returns the first photo.
	IdempotencyKey string
	Data           []byte
}

// Upload checks, processes and stores a photo for a property the user may write to.
func (s *Photos) Upload(ctx context.Context, userID uuid.UUID, in Upload) (storage.Photo, error) {
	if in.IdempotencyKey != "" {
		existing, err := s.repo.FindByIdempotencyKey(ctx, userID, in.IdempotencyKey)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return storage.Photo{}, unavailable(err)
		}
	}
	if err := s.requireAccess(ctx, in.PropertyID, userID, core.Write); err != nil {
		return storage.Photo{}, err
	}
	result, err := s.process(ctx, in.Data)
	if err != nil {
		return storage.Photo{}, err
	}
	photo := s.newPhoto(userID, in, result)
	if err := s.storeFiles(ctx, photo, result); err != nil {
		return storage.Photo{}, err
	}
	err = s.repo.Insert(ctx, photo)
	if errors.Is(err, storage.ErrDuplicateIdempotencyKey) {
		// A concurrent retry with the same key won the race: keep its photo, drop this one.
		s.removeFiles(ctx, photo)
		return s.repo.FindByIdempotencyKey(ctx, userID, in.IdempotencyKey)
	}
	if err != nil {
		s.removeFiles(ctx, photo)
		return storage.Photo{}, unavailable(err)
	}
	return photo, nil
}

// Content opens a variant of a photo the user may read; the caller closes the reader.
func (s *Photos) Content(ctx context.Context, userID, photoID uuid.UUID, variant string) (io.ReadCloser, int64, error) {
	photo, err := s.repo.Get(ctx, photoID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, unavailable(err)
	}
	if err := s.requireAccess(ctx, photo.PropertyID, userID, core.Read); err != nil {
		return nil, 0, err
	}
	body, size, err := s.files.Get(ctx, keyOf(photo, variant))
	if errors.Is(err, objectstore.ErrNotFound) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, unavailable(err)
	}
	return body, size, nil
}

// Delete removes an unattached photo uploaded by the user, with its files.
func (s *Photos) Delete(ctx context.Context, userID, photoID uuid.UUID) error {
	photo, err := s.repo.DeleteUploaded(ctx, photoID, userID)
	if errors.Is(err, storage.ErrNotFound) {
		return s.whyNotDeleted(ctx, userID, photoID)
	}
	if err != nil {
		return unavailable(err)
	}
	s.removeFiles(ctx, photo)
	return nil
}

func (s *Photos) whyNotDeleted(ctx context.Context, userID, photoID uuid.UUID) error {
	photo, err := s.repo.Get(ctx, photoID)
	switch {
	case errors.Is(err, storage.ErrNotFound) || (err == nil && photo.OwnerID != userID):
		return ErrNotFound
	case err != nil:
		return unavailable(err)
	default:
		return ErrAttached
	}
}

func (s *Photos) requireAccess(ctx context.Context, propertyID, userID uuid.UUID, action core.Action) error {
	allowed, err := s.access.CanAccess(ctx, propertyID, userID, action)
	if err != nil {
		return unavailable(err)
	}
	if !allowed {
		return ErrNotFound
	}
	return nil
}

// process decodes and re-encodes the image, at most Workers at a time to bound memory use.
func (s *Photos) process(ctx context.Context, data []byte) (imaging.Result, error) {
	select {
	case s.workers <- struct{}{}:
	case <-ctx.Done():
		return imaging.Result{}, fmt.Errorf("wait for image worker: %w", ctx.Err())
	}
	defer func() { <-s.workers }()
	return imaging.Process(data, s.limits)
}

func (s *Photos) newPhoto(userID uuid.UUID, in Upload, result imaging.Result) storage.Photo {
	id := uuid.Must(uuid.NewV7())
	prefix := in.PropertyID.String() + "/" + id.String() + "/"
	sum := sha256.Sum256(result.Original.Data)
	var key *string
	if in.IdempotencyKey != "" {
		key = &in.IdempotencyKey
	}
	return storage.Photo{
		ID:             id,
		OwnerID:        userID,
		PropertyID:     in.PropertyID,
		Status:         storage.StatusUploaded,
		OriginalKey:    prefix + VariantOriginal + ".jpg",
		PreviewKey:     prefix + VariantPreview + ".jpg",
		ThumbKey:       prefix + VariantThumb + ".jpg",
		ContentType:    string(imaging.JPEG),
		SizeBytes:      int64(len(result.Original.Data)),
		Width:          result.Original.Width,
		Height:         result.Original.Height,
		SHA256:         hex.EncodeToString(sum[:]),
		IdempotencyKey: key,
		CreatedAt:      s.now().UTC().Truncate(time.Microsecond),
	}
}

func (s *Photos) storeFiles(ctx context.Context, photo storage.Photo, result imaging.Result) error {
	variants := []struct {
		key  string
		data []byte
	}{
		{photo.OriginalKey, result.Original.Data},
		{photo.PreviewKey, result.Preview.Data},
		{photo.ThumbKey, result.Thumb.Data},
	}
	for _, v := range variants {
		if err := s.files.Put(ctx, v.key, v.data); err != nil {
			s.removeFiles(ctx, photo)
			return unavailable(err)
		}
	}
	return nil
}

// removeFiles deletes the files of a photo. A failure leaves orphan files, which only take space:
// no metadata points to them, so they are never served. It is logged for the operator.
func (s *Photos) removeFiles(ctx context.Context, photo storage.Photo) {
	if err := s.files.Delete(context.WithoutCancel(ctx), photo.Keys()...); err != nil {
		s.logger.WarnContext(ctx, "photo files not deleted", slog.String("photo.id", photo.ID.String()),
			slog.Any("error", err))
	}
}

func keyOf(photo storage.Photo, variant string) string {
	switch variant {
	case VariantOriginal:
		return photo.OriginalKey
	case VariantThumb:
		return photo.ThumbKey
	default:
		return photo.PreviewKey
	}
}

func unavailable(err error) error {
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}
