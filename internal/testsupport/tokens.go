package testsupport

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TokenIssuer signs access tokens like core-service does and serves its public key as JWKS.
type TokenIssuer struct {
	key *rsa.PrivateKey
	// JWKSURL serves the public key.
	JWKSURL string
}

// TokenKeyID is the key id of the test signing key.
const TokenKeyID = "test-key"

// NewTokenIssuer generates a key and starts a JWKS server that stops when the test ends.
func NewTokenIssuer(t *testing.T) *TokenIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	jwks := map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": TokenKeyID,
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(server.Close)
	return &TokenIssuer{key: key, JWKSURL: server.URL}
}

// Token returns a valid access token of the user with the given role, as issued by core.
func (i *TokenIssuer) Token(t *testing.T, userID uuid.UUID, role string) string {
	t.Helper()
	return i.Sign(t, jwt.MapClaims{
		"iss":  "inspecto-core",
		"aud":  []string{"inspecto-api"},
		"sub":  userID.String(),
		"role": role,
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(15 * time.Minute).Unix(),
	})
}

// Sign signs arbitrary claims with the test key.
func (i *TokenIssuer) Sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = TokenKeyID
	signed, err := token.SignedString(i.key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// ServiceToken returns a valid service token of the named service with the given scopes.
func (i *TokenIssuer) ServiceToken(t *testing.T, service, scope string) string {
	t.Helper()
	return i.Sign(t, jwt.MapClaims{
		"iss":   "inspecto-core",
		"aud":   []string{"inspecto-internal"},
		"sub":   service,
		"scope": scope,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(15 * time.Minute).Unix(),
	})
}
