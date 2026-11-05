// Package auth verifies user access tokens issued by core-service (ADR-007): RS256 signature with a
// key from the JWKS of core, issuer, audience and expiry.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/config"
)

// Roles of users (ADR-009).
const (
	RoleAdmin     = "ADMIN"
	RoleInspector = "INSPECTOR"
)

// clockSkew tolerates small clock differences between core-service and media-service.
const clockSkew = 30 * time.Second

var (
	// ErrInvalidToken means the token is missing, malformed, not signed by core or not meant for
	// this API.
	ErrInvalidToken = errors.New("invalid access token")
	// ErrExpiredToken means the token was valid but has expired; the client should refresh it.
	ErrExpiredToken = errors.New("access token expired")
)

// User is the caller identified by a verified token. The id is taken only from the token, never
// from the request (SECURITY.md §3).
type User struct {
	ID   uuid.UUID
	Role string
}

// Verifier checks access tokens.
type Verifier struct {
	keys   jwt.Keyfunc
	parser *jwt.Parser
}

type claims struct {
	jwt.RegisteredClaims
	Role string `json:"role"`
}

// NewVerifier loads the signing keys from the JWKS URL of core-service and keeps them up to date
// in the background until ctx is cancelled; an unknown key id triggers a refresh, so key rotation
// in core needs no restart here.
func NewVerifier(ctx context.Context, cfg config.Auth) (*Verifier, error) {
	keys, err := keyfunc.NewDefaultCtx(ctx, []string{cfg.JWKSURL})
	if err != nil {
		return nil, fmt.Errorf("load JWKS: %w", err)
	}
	return newVerifier(keys.Keyfunc, cfg), nil
}

func newVerifier(keys jwt.Keyfunc, cfg config.Auth) *Verifier {
	return &Verifier{
		keys: keys,
		parser: jwt.NewParser(
			// Only RS256: protects against alg=none and algorithm substitution (SECURITY.md §2).
			jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
			jwt.WithIssuer(cfg.Issuer),
			jwt.WithAudience(cfg.Audience),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(clockSkew),
		),
	}
}

// Verify returns the user of a valid token.
func (v *Verifier) Verify(token string) (User, error) {
	var c claims
	if _, err := v.parser.ParseWithClaims(token, &c, v.keys); err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return User{}, ErrExpiredToken
		}
		return User{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return User{}, fmt.Errorf("%w: subject is not a user id", ErrInvalidToken)
	}
	return User{ID: id, Role: c.Role}, nil
}

// CanManageProperties tells whether the role may work with photos of properties; clients with
// read-only access arrive with the CLIENT role in the extension stage (ADR-009).
func (u User) CanManageProperties() bool {
	return u.Role == RoleInspector || u.Role == RoleAdmin
}
