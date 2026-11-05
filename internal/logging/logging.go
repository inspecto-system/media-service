// Package logging builds the structured JSON logger of the service. Field names follow Elastic
// Common Schema, as the logs of core-service do, so that logs of all services read alike.
package logging

import (
	"io"
	"log/slog"
)

// ServiceName identifies the service in every log record.
const ServiceName = "media-service"

// New returns a JSON logger writing to w. Passwords, tokens and personal data must never be passed
// to it (SECURITY.md §5); the service logs only identifiers.
func New(w io.Writer) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		ReplaceAttr: toECS,
	})
	return slog.New(handler).With(slog.String("service.name", ServiceName))
}

func toECS(groups []string, attr slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return attr
	}
	switch attr.Key {
	case slog.TimeKey:
		attr.Key = "@timestamp"
	case slog.LevelKey:
		attr.Key = "log.level"
	case slog.MessageKey:
		attr.Key = "message"
	}
	return attr
}
