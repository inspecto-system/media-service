// Package auth verifies tokens issued by core-service (ADR-007): RS256 signature with a key from
// the JWKS of core, issuer, audience and expiry. User access tokens and service tokens differ only
// in the audience, so a token of one kind is never accepted as the other.
package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// Scopes of service tokens (docs/API_DOCUMENTATION.md, internal endpoints).
const (
	ScopePhotosManage = "photos.manage"
	ScopePhotosRead   = "photos.read"
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

// Service is the service that called an internal endpoint.
type Service struct {
	// Name is the client id, for example core-service.
	Name   string
	Scopes []string
}

// Verifier checks access tokens.
type Verifier struct {
	keys     jwt.Keyfunc
	users    *jwt.Parser
	services *jwt.Parser
}

type claims struct {
	jwt.RegisteredClaims
	Role  string `json:"role"`
	Scope string `json:"scope"`
}

// NewVerifier loads the signing keys from the JWKS URL of core-service and keeps them up to date
// in the background until ctx is cancelled; an unknown key id triggers a refresh, so key rotation
// in core needs no restart here.
func NewVerifier(ctx context.Context, cfg config.Auth) (*Verifier, error) {
	keys, err := keyfunc.NewDefaultCtx(ctx, []string{cfg.JWKSURL})
	if err != nil {
		return nil, fmt.Errorf("load JWKS: %w", err)
	}
	return &Verifier{
		keys:     keys.Keyfunc,
		users:    parser(cfg.Issuer, cfg.Audience),
		services: parser(cfg.Issuer, cfg.InternalAudience),
	}, nil
}

func parser(issuer, audience string) *jwt.Parser {
	return jwt.NewParser(
		// Only RS256: protects against alg=none and algorithm substitution (SECURITY.md §2).
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(clockSkew),
	)
}

// Verify returns the user of a valid user access token.
func (v *Verifier) Verify(token string) (User, error) {
	c, err := v.parse(v.users, token)
	if err != nil {
		return User{}, err
	}
	id, err := uuid.Parse(c.Subject)
	if err != nil {
		return User{}, fmt.Errorf("%w: subject is not a user id", ErrInvalidToken)
	}
	return User{ID: id, Role: c.Role}, nil
}

// VerifyService returns the caller of a valid service token.
func (v *Verifier) VerifyService(token string) (Service, error) {
	c, err := v.parse(v.services, token)
	if err != nil {
		return Service{}, err
	}
	if c.Subject == "" {
		return Service{}, fmt.Errorf("%w: no subject", ErrInvalidToken)
	}
	return Service{Name: c.Subject, Scopes: strings.Fields(c.Scope)}, nil
}

func (v *Verifier) parse(p *jwt.Parser, token string) (claims, error) {
	var c claims
	if _, err := p.ParseWithClaims(token, &c, v.keys); err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return claims{}, ErrExpiredToken
		}
		return claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	return c, nil
}

// CanManageProperties tells whether the role may work with photos of properties; clients with
// read-only access arrive with the CLIENT role in the extension stage (ADR-009).
func (u User) CanManageProperties() bool {
	return u.Role == RoleInspector || u.Role == RoleAdmin
}

// Has tells whether the token grants the scope.
func (s Service) Has(scope string) bool {
	return slices.Contains(s.Scopes, scope)
}
