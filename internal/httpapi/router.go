package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inspecto-system/media-service/internal/auth"
)

// readinessTimeout bounds the dependency checks of /health/ready.
const readinessTimeout = 2 * time.Second

// Pinger checks that a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Dependencies are the parts the HTTP layer works with.
type Dependencies struct {
	Logger *slog.Logger
	// Ready lists the dependencies checked by /health/ready.
	Ready    []Pinger
	Verifier TokenVerifier
	Photos   PhotoService
	Limits   Limits
	// Services verifies service tokens of /internal/** (not routed by nginx, ADR-013).
	Services ServiceVerifier
	Internal InternalService
}

// NewRouter builds the HTTP handler of the service. External endpoints are served under /api/v1,
// as routed by nginx (ADR-013); /internal/v1 accepts only service tokens.
func NewRouter(deps Dependencies) http.Handler {
	r := chi.NewRouter()
	r.Use(withRequestID, withSecurityHeaders, withAccessLog(deps.Logger), withRecovery(deps.Logger))
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "Resource not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		WriteProblem(w, r, http.StatusMethodNotAllowed, CodeValidation, "Method not allowed")
	})
	r.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
	})
	r.Get("/health/ready", readiness(deps.Logger, deps.Ready))

	photos := photoHandlers{photos: deps.Photos, limits: deps.Limits, logger: deps.Logger}
	r.Route("/api/v1/photos", func(r chi.Router) {
		r.Use(authenticated(deps.Verifier))
		r.Get("/limits", photos.getLimits)
		r.Post("/", photos.upload)
		r.Get("/{photoId}/content", photos.content)
		r.Delete("/{photoId}", photos.delete)
	})

	internal := internalHandlers{photos: deps.Internal, logger: deps.Logger}
	r.Route("/internal/v1/photos", func(r chi.Router) {
		r.With(serviceAuthenticated(deps.Services, auth.ScopePhotosManage)).Group(func(r chi.Router) {
			r.Get("/{photoId}", internal.metadata)
			r.Post("/{photoId}/attach", internal.attach)
			r.Delete("/{photoId}", internal.delete)
			r.Delete("/", internal.deleteInBulk)
		})
		r.With(serviceAuthenticated(deps.Services, auth.ScopePhotosRead)).Get("/{photoId}/content", internal.content)
	})
	return r
}

// readiness reports UP when every dependency answers; otherwise DOWN with 503, so that docker
// compose and nginx stop routing to the instance. The cause is logged, not returned.
func readiness(logger *slog.Logger, dependencies []Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()
		for _, dependency := range dependencies {
			if err := dependency.Ping(ctx); err != nil {
				logger.WarnContext(r.Context(), "dependency is not ready", slog.Any("error", err))
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "DOWN"})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
	}
}
