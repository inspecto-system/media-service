// Package app wires the service together and runs it until the context is cancelled.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/inspecto-system/media-service/internal/auth"
	"github.com/inspecto-system/media-service/internal/config"
	"github.com/inspecto-system/media-service/internal/core"
	"github.com/inspecto-system/media-service/internal/httpapi"
	"github.com/inspecto-system/media-service/internal/imaging"
	"github.com/inspecto-system/media-service/internal/jobs"
	"github.com/inspecto-system/media-service/internal/objectstore"
	"github.com/inspecto-system/media-service/internal/service"
	"github.com/inspecto-system/media-service/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Timeouts of the HTTP server; they limit slow clients (SECURITY.md §4, resource consumption).
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 60 * time.Second
	idleTimeout       = 120 * time.Second
	maxHeaderBytes    = 64 << 10
	shutdownTimeout   = 20 * time.Second
	// cleanupInterval is how often unattached photos older than the TTL are looked for.
	cleanupInterval = time.Hour
)

// Run migrates the database, then serves HTTP on the listener until ctx is cancelled and the
// in-flight requests have finished.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger, listener net.Listener) error {
	pool, err := prepareDatabase(ctx, cfg.Database)
	if err != nil {
		// Nothing was served yet; release the port for the caller.
		_ = listener.Close()
		return err
	}
	defer pool.Close()
	verifier, err := auth.NewVerifier(ctx, cfg.Auth)
	if err != nil {
		_ = listener.Close()
		return err
	}

	repo := storage.NewPhotos(pool)
	files := objectstore.New(cfg.Storage)
	photos := service.NewPhotos(repo, files, core.New(cfg.Core), service.Settings{
		Limits:  imaging.Limits{MaxPixels: cfg.Photos.MaxPixels},
		Workers: cfg.Photos.Workers,
		Logger:  logger,
	})
	go jobs.NewCleaner(repo, files, cfg.Photos.UnattachedTTL, cleanupInterval, logger).Run(ctx)

	server := &http.Server{
		Handler: httpapi.NewRouter(httpapi.Dependencies{
			Logger:   logger,
			Ready:    []httpapi.Pinger{pool, files},
			Verifier: verifier,
			Photos:   photos,
			Limits: httpapi.Limits{
				MaxFileSizeBytes:        cfg.Photos.MaxFileSizeBytes,
				MaxPixels:               cfg.Photos.MaxPixels,
				MaxPhotosPerObservation: cfg.Photos.MaxPhotosPerObservation,
			},
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.InfoContext(ctx, "media-service started", slog.String("address", listener.Addr().String()))

	select {
	case err := <-served:
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down HTTP server: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	logger.InfoContext(ctx, "media-service stopped")
	return nil
}

// prepareDatabase applies migrations as the schema owner and connects as the DML-only role.
func prepareDatabase(ctx context.Context, db config.Database) (*pgxpool.Pool, error) {
	migratorURL, err := db.URLFor(db.Migrator)
	if err != nil {
		return nil, err
	}
	if err := storage.Migrate(migratorURL); err != nil {
		return nil, err
	}
	appURL, err := db.URLFor(db.App)
	if err != nil {
		return nil, err
	}
	return storage.Connect(ctx, appURL)
}
