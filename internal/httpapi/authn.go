package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/inspecto-system/media-service/internal/auth"
)

// TokenVerifier checks user access tokens.
type TokenVerifier interface {
	Verify(token string) (auth.User, error)
}

type userKey struct{}

func userFrom(ctx context.Context) auth.User {
	user, _ := ctx.Value(userKey{}).(auth.User)
	return user
}

// authenticated admits requests with a valid access token of a role that works with properties.
func authenticated(verifier TokenVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				unauthenticated(w, r, CodeUnauthenticated)
				return
			}
			user, err := verifier.Verify(token)
			if errors.Is(err, auth.ErrExpiredToken) {
				unauthenticated(w, r, CodeTokenExpired)
				return
			}
			if err != nil {
				unauthenticated(w, r, CodeUnauthenticated)
				return
			}
			if !user.CanManageProperties() {
				WriteProblem(w, r, http.StatusForbidden, CodeForbidden, "Access denied")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
		})
	}
}

func unauthenticated(w http.ResponseWriter, r *http.Request, code Code) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	WriteProblem(w, r, http.StatusUnauthorized, code, "Authentication is required")
}
