package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// readinessTimeout bounds the dependency checks of /health/ready.
const readinessTimeout = 2 * time.Second

// Pinger checks that a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewRouter builds the HTTP handler of the service. Endpoints of later tasks are mounted here.
func NewRouter(logger *slog.Logger, database Pinger) http.Handler {
	r := chi.NewRouter()
	r.Use(withRequestID, withAccessLog(logger), withRecovery(logger))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "Resource not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		WriteProblem(w, r, http.StatusMethodNotAllowed, CodeValidation, "Method not allowed")
	})
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeHealth(w, http.StatusOK, "UP")
	})
	r.Get("/health/ready", readiness(logger, database))
	return r
}

// readiness reports UP when the database answers; otherwise DOWN with 503, so that docker compose
// and nginx stop routing to the instance. The cause is logged, not returned.
func readiness(logger *slog.Logger, database Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()
		if err := database.Ping(ctx); err != nil {
			logger.WarnContext(r.Context(), "database is not ready", slog.Any("error", err))
			writeHealth(w, http.StatusServiceUnavailable, "DOWN")
			return
		}
		writeHealth(w, http.StatusOK, "UP")
	}
}

func writeHealth(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is already sent; a failed write means the client has gone and nothing is left to do.
	_ = json.NewEncoder(w).Encode(map[string]string{"status": state})
}
