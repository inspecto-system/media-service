package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/config"
	"github.com/inspecto-system/media-service/internal/testsupport"
)

func verifier(t *testing.T, issuer *testsupport.TokenIssuer) *Verifier {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v, err := NewVerifier(ctx, config.Auth{
		JWKSURL: issuer.JWKSURL, Issuer: "inspecto-core", Audience: "inspecto-api",
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func claimsWith(change func(jwt.MapClaims)) jwt.MapClaims {
	c := jwt.MapClaims{
		"iss":  "inspecto-core",
		"aud":  []string{"inspecto-api"},
		"sub":  uuid.NewString(),
		"role": RoleInspector,
		"exp":  time.Now().Add(time.Minute).Unix(),
	}
	change(c)
	return c
}

func TestVerifyAcceptsTokenOfCore(t *testing.T) {
	issuer := testsupport.NewTokenIssuer(t)
	id := uuid.New()

	user, err := verifier(t, issuer).Verify(issuer.Token(t, id, RoleInspector))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if user.ID != id || user.Role != RoleInspector || !user.CanManageProperties() {
		t.Errorf("user = %+v", user)
	}
}

func TestVerifyRejectsForeignOrBrokenTokens(t *testing.T) {
	issuer := testsupport.NewTokenIssuer(t)
	v := verifier(t, issuer)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	foreign := jwt.NewWithClaims(jwt.SigningMethodRS256, claimsWith(func(jwt.MapClaims) {}))
	foreign.Header["kid"] = testsupport.TokenKeyID
	foreignSigned, err := foreign.SignedString(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, claimsWith(func(jwt.MapClaims) {})).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"garbage":            "not.a.token",
		"signed by stranger": foreignSigned,
		"alg none":           none,
		"wrong issuer":       issuer.Sign(t, claimsWith(func(c jwt.MapClaims) { c["iss"] = "evil" })),
		"service audience":   issuer.Sign(t, claimsWith(func(c jwt.MapClaims) { c["aud"] = []string{"inspecto-internal"} })),
		"no expiry":          issuer.Sign(t, claimsWith(func(c jwt.MapClaims) { delete(c, "exp") })),
		"subject not uuid":   issuer.Sign(t, claimsWith(func(c jwt.MapClaims) { c["sub"] = "admin" })),
		"not yet valid":      issuer.Sign(t, claimsWith(func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(time.Hour).Unix() })),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerifyReportsExpiredToken(t *testing.T) {
	issuer := testsupport.NewTokenIssuer(t)
	token := issuer.Sign(t, claimsWith(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }))

	if _, err := verifier(t, issuer).Verify(token); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("err = %v, want ErrExpiredToken", err)
	}
}

func TestOnlyInspectorsAndAdminsManageProperties(t *testing.T) {
	for role, want := range map[string]bool{RoleInspector: true, RoleAdmin: true, "CLIENT": false, "": false} {
		if got := (User{Role: role}).CanManageProperties(); got != want {
			t.Errorf("role %q: %v, want %v", role, got, want)
		}
	}
}

func TestNewVerifierRejectsInvalidURL(t *testing.T) {
	if _, err := NewVerifier(context.Background(), config.Auth{JWKSURL: "::"}); err == nil {
		t.Error("want an error")
	}
}
