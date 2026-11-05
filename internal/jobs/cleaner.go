// Package jobs runs background work of the service.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/inspecto-system/media-service/internal/storage"
)

// batchSize bounds one cleaning round, so a long backlog does not hold locks for long.
const batchSize = 100

// Expired takes unattached photos older than a moment, removing their metadata.
type Expired interface {
	TakeExpired(ctx context.Context, before time.Time, limit int) ([]storage.Photo, error)
}

// FileRemover deletes photo files.
type FileRemover interface {
	Delete(ctx context.Context, keys ...string) error
}

// Cleaner deletes photos that were not attached to an observation in time (ADR-018).
type Cleaner struct {
	photos   Expired
	files    FileRemover
	ttl      time.Duration
	interval time.Duration
	logger   *slog.Logger
	now      func() time.Time
}

// NewCleaner returns a cleaner that runs every interval and removes photos older than ttl.
func NewCleaner(photos Expired, files FileRemover, ttl, interval time.Duration, logger *slog.Logger) *Cleaner {
	return &Cleaner{photos: photos, files: files, ttl: ttl, interval: interval, logger: logger, now: time.Now}
}

// Run cleans at start and then every interval until ctx is cancelled.
func (c *Cleaner) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		c.clean(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// clean removes expired photos in batches until none are left or an error occurs. Metadata goes
// first, in its own statement: once deleted, a photo can no longer be attached or served, and
// files left behind by a failed deletion are only logged.
func (c *Cleaner) clean(ctx context.Context) int {
	removed := 0
	for ctx.Err() == nil {
		expired, err := c.photos.TakeExpired(ctx, c.now().Add(-c.ttl), batchSize)
		if err != nil {
			c.logger.ErrorContext(ctx, "clean unattached photos", slog.Any("error", err))
			return removed
		}
		for _, photo := range expired {
			if err := c.files.Delete(ctx, photo.Keys()...); err != nil {
				c.logger.WarnContext(ctx, "photo files not deleted",
					slog.String("photo.id", photo.ID.String()), slog.Any("error", err))
			}
		}
		removed += len(expired)
		if len(expired) < batchSize {
			break
		}
	}
	if removed > 0 {
		c.logger.InfoContext(ctx, "unattached photos deleted", slog.Int("count", removed))
	}
	return removed
}
