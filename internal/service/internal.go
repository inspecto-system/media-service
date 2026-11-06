package service

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/objectstore"
	"github.com/inspecto-system/media-service/internal/storage"
)

// InternalRepository is the part of the photo repository used by other services' calls.
type InternalRepository interface {
	Get(ctx context.Context, id uuid.UUID) (storage.Photo, error)
	Attach(ctx context.Context, id uuid.UUID, at time.Time) (storage.Photo, error)
	ListByProperty(ctx context.Context, propertyID uuid.UUID) ([]storage.Photo, error)
	ListByOwner(ctx context.Context, ownerID uuid.UUID) ([]storage.Photo, error)
	DeleteByIDs(ctx context.Context, ids []uuid.UUID) error
}

// Internal implements the operations other services call on /internal/v1/photos. The caller is
// authenticated by its service token; users' rights were checked by that service (core-service).
type Internal struct {
	repo  InternalRepository
	files Files
	now   func() time.Time
}

// NewInternal returns the service.
func NewInternal(repo InternalRepository, files Files) *Internal {
	return &Internal{repo: repo, files: files, now: time.Now}
}

// Metadata returns a photo; core-service checks owner and property before attaching it.
func (s *Internal) Metadata(ctx context.Context, photoID uuid.UUID) (storage.Photo, error) {
	photo, err := s.repo.Get(ctx, photoID)
	if errors.Is(err, storage.ErrNotFound) {
		return storage.Photo{}, ErrNotFound
	}
	if err != nil {
		return storage.Photo{}, unavailable(err)
	}
	return photo, nil
}

// Attach marks a photo as attached, so the clean-up job keeps it; repeating it changes nothing.
func (s *Internal) Attach(ctx context.Context, photoID uuid.UUID) error {
	_, err := s.repo.Attach(ctx, photoID, s.now().UTC().Truncate(time.Microsecond))
	if errors.Is(err, storage.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return unavailable(err)
	}
	return nil
}

// Content opens a variant of any photo, for report-service and ml-service.
func (s *Internal) Content(ctx context.Context, photoID uuid.UUID, variant string) (io.ReadCloser, int64, error) {
	photo, err := s.Metadata(ctx, photoID)
	if err != nil {
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

// Delete removes a photo with its files; a missing photo is not an error, so retries succeed.
func (s *Internal) Delete(ctx context.Context, photoID uuid.UUID) error {
	photo, err := s.repo.Get(ctx, photoID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}
	return s.remove(ctx, []storage.Photo{photo})
}

// DeleteProperty removes all photos of a property (ADR-018, property deleted in core).
func (s *Internal) DeleteProperty(ctx context.Context, propertyID uuid.UUID) error {
	photos, err := s.repo.ListByProperty(ctx, propertyID)
	if err != nil {
		return unavailable(err)
	}
	return s.remove(ctx, photos)
}

// DeleteOwner removes all photos uploaded by a user (ADR-018, account deleted in core).
func (s *Internal) DeleteOwner(ctx context.Context, ownerID uuid.UUID) error {
	photos, err := s.repo.ListByOwner(ctx, ownerID)
	if err != nil {
		return unavailable(err)
	}
	return s.remove(ctx, photos)
}

// remove deletes the files first and the rows only after that: if a file cannot be deleted the
// call fails, the rows stay, and the retry of the caller finds the files again.
func (s *Internal) remove(ctx context.Context, photos []storage.Photo) error {
	if len(photos) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(photos))
	keys := make([]string, 0, 3*len(photos))
	for _, photo := range photos {
		ids = append(ids, photo.ID)
		keys = append(keys, photo.Keys()...)
	}
	if err := s.files.Delete(ctx, keys...); err != nil {
		return unavailable(err)
	}
	if err := s.repo.DeleteByIDs(ctx, ids); err != nil {
		return unavailable(err)
	}
	return nil
}
