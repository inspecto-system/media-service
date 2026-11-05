package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// RequestIDHeader carries the id that ties together the logs of one request across services.
const RequestIDHeader = "X-Request-Id"

// validRequestID matches the ids core-service accepts, so that ids pass through unchanged.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

type requestIDKey struct{}

// RequestIDFrom returns the id of the current request, or "" outside a request.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// withRequestID takes a valid incoming X-Request-Id or generates one, and returns it in the
// response (docs/API_DOCUMENTATION.md §1).
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// statusRecorder remembers the status code written by a handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

// withAccessLog logs one record per request. Query strings are not logged: they may contain
// personal data.
func withAccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			logger.InfoContext(r.Context(), "request",
				slog.String("http.request.method", r.Method),
				slog.String("url.path", r.URL.Path),
				slog.Int("http.response.status_code", recorder.status),
				slog.Int64("event.duration_ms", time.Since(started).Milliseconds()),
				slog.String("trace.id", RequestIDFrom(r.Context())))
		})
	}
}

// withRecovery turns a panic into a 500 problem response and logs it with the request id.
func withRecovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(recovered) // the server aborts the response on purpose
					}
					logger.ErrorContext(r.Context(), "handler panicked",
						slog.Any("error", recovered),
						slog.String("trace.id", RequestIDFrom(r.Context())))
					WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Unexpected error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// withSecurityHeaders stops browsers from guessing content types: an uploaded file is always served
// as the type the service set (SECURITY.md §4).
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
