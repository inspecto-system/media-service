package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Photo statuses (docs/DATABASE.md §4).
const (
	StatusUploaded = "UPLOADED"
	StatusAttached = "ATTACHED"
)

var (
	// ErrNotFound means there is no such photo.
	ErrNotFound = errors.New("photo not found")
	// ErrDuplicateIdempotencyKey means the owner already uploaded a photo with this key.
	ErrDuplicateIdempotencyKey = errors.New("idempotency key already used")
)

// Photo is a row of the photos table.
type Photo struct {
	ID             uuid.UUID
	OwnerID        uuid.UUID
	PropertyID     uuid.UUID
	Status         string
	OriginalKey    string
	PreviewKey     string
	ThumbKey       string
	ContentType    string
	SizeBytes      int64
	Width          int
	Height         int
	SHA256         string
	IdempotencyKey *string
	CreatedAt      time.Time
	AttachedAt     *time.Time
}

// Keys returns the object keys of all variants.
func (p Photo) Keys() []string {
	return []string{p.OriginalKey, p.PreviewKey, p.ThumbKey}
}

const photoColumns = `id, owner_id, property_id, status, original_key, preview_key, thumb_key,
	content_type, size_bytes, width, height, sha256, idempotency_key, created_at, attached_at`

// uniqueViolation is the PostgreSQL error code of a unique constraint violation.
const uniqueViolation = "23505"

// Photos is the repository of photo metadata. All queries use placeholders (SECURITY.md §4).
type Photos struct {
	pool *pgxpool.Pool
}

// NewPhotos returns the repository.
func NewPhotos(pool *pgxpool.Pool) *Photos {
	return &Photos{pool: pool}
}

// Insert stores a new photo.
func (r *Photos) Insert(ctx context.Context, p Photo) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO photos (`+photoColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		p.ID, p.OwnerID, p.PropertyID, p.Status, p.OriginalKey, p.PreviewKey, p.ThumbKey,
		p.ContentType, p.SizeBytes, p.Width, p.Height, p.SHA256, p.IdempotencyKey, p.CreatedAt, p.AttachedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == "photos_owner_idempotency_uq" {
		return ErrDuplicateIdempotencyKey
	}
	if err != nil {
		return fmt.Errorf("insert photo: %w", err)
	}
	return nil
}

// Get returns a photo by id.
func (r *Photos) Get(ctx context.Context, id uuid.UUID) (Photo, error) {
	return r.one(ctx, `SELECT `+photoColumns+` FROM photos WHERE id = $1`, id)
}

// FindByIdempotencyKey returns the photo the owner uploaded with the key.
func (r *Photos) FindByIdempotencyKey(ctx context.Context, ownerID uuid.UUID, key string) (Photo, error) {
	return r.one(ctx, `SELECT `+photoColumns+` FROM photos WHERE owner_id = $1 AND idempotency_key = $2`, ownerID, key)
}

// DeleteUploaded deletes an unattached photo of the owner and returns it. ErrNotFound covers a
// missing photo, a photo of someone else and an attached photo; the caller tells them apart.
func (r *Photos) DeleteUploaded(ctx context.Context, id, ownerID uuid.UUID) (Photo, error) {
	return r.one(ctx, `DELETE FROM photos WHERE id = $1 AND owner_id = $2 AND status = 'UPLOADED'
		RETURNING `+photoColumns, id, ownerID)
}

// TakeExpired deletes up to limit unattached photos created before the given time and returns
// them so their files can be removed. Rows locked by a concurrent attach are skipped.
func (r *Photos) TakeExpired(ctx context.Context, before time.Time, limit int) ([]Photo, error) {
	rows, err := r.pool.Query(ctx, `DELETE FROM photos WHERE id IN (
			SELECT id FROM photos WHERE status = 'UPLOADED' AND created_at < $1
			ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING `+photoColumns, before, limit)
	if err != nil {
		return nil, fmt.Errorf("delete expired photos: %w", err)
	}
	photos, err := pgx.CollectRows(rows, scanPhoto)
	if err != nil {
		return nil, fmt.Errorf("delete expired photos: %w", err)
	}
	return photos, nil
}

func (r *Photos) one(ctx context.Context, query string, args ...any) (Photo, error) {
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return Photo{}, fmt.Errorf("query photo: %w", err)
	}
	photo, err := pgx.CollectExactlyOneRow(rows, scanPhoto)
	if errors.Is(err, pgx.ErrNoRows) {
		return Photo{}, ErrNotFound
	}
	if err != nil {
		return Photo{}, fmt.Errorf("query photo: %w", err)
	}
	return photo, nil
}

func scanPhoto(row pgx.CollectableRow) (Photo, error) {
	var p Photo
	err := row.Scan(&p.ID, &p.OwnerID, &p.PropertyID, &p.Status, &p.OriginalKey, &p.PreviewKey, &p.ThumbKey,
		&p.ContentType, &p.SizeBytes, &p.Width, &p.Height, &p.SHA256, &p.IdempotencyKey, &p.CreatedAt, &p.AttachedAt)
	return p, err
}
