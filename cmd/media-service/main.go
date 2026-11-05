// Command media-service receives, checks, stores and serves photos of Inspecto (ADR-002).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/inspecto-system/media-service/internal/app"
	"github.com/inspecto-system/media-service/internal/config"
	"github.com/inspecto-system/media-service/internal/logging"
)

func main() {
	logger := logging.New(os.Stdout)
	if err := run(logger); err != nil {
		logger.Error("media-service failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var lc net.ListenConfig
	// The service runs in a container behind nginx and must accept connections on all interfaces.
	listener, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return app.Run(ctx, cfg, logger, listener)
}
