package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/config"
)

// fakeCore imitates the two internal endpoints of core-service used by media-service.
type fakeCore struct {
	tokens   atomic.Int32
	checks   atomic.Int32
	expires  int
	access   func(r *http.Request) int
	tokenErr int
}

func (f *fakeCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/internal/v1/auth/service-token":
		f.tokens.Add(1)
		if f.tokenErr != 0 {
			w.WriteHeader(f.tokenErr)
			return
		}
		if r.FormValue("client_id") != "media-service" || r.FormValue("client_secret") != "secret" ||
			r.FormValue("grant_type") != "client_credentials" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","token_type":"Bearer","expires_in":%d}`, f.tokens.Load(), f.expires)
	default:
		f.checks.Add(1)
		w.WriteHeader(f.access(r))
	}
}

func newClient(t *testing.T, fake *fakeCore) (*Client, *time.Time) {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	c := New(config.Core{URL: server.URL + "/", ClientID: "media-service", ClientSecret: "secret"})
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, &now
}

func allowOwner(owner uuid.UUID) func(r *http.Request) int {
	return func(r *http.Request) int {
		if r.Header.Get("Authorization") == "" || r.URL.Query().Get("userId") != owner.String() {
			return http.StatusNotFound
		}
		return http.StatusOK
	}
}

func TestCanAccessAsksCoreAndCachesPositiveAnswers(t *testing.T) {
	owner := uuid.New()
	fake := &fakeCore{expires: 900, access: allowOwner(owner)}
	c, now := newClient(t, fake)
	property := uuid.New()

	for range 3 {
		ok, err := c.CanAccess(context.Background(), property, owner, Write)
		if err != nil || !ok {
			t.Fatalf("CanAccess = %v, %v", ok, err)
		}
	}
	if fake.checks.Load() != 1 || fake.tokens.Load() != 1 {
		t.Errorf("checks %d, tokens %d; want 1 and 1", fake.checks.Load(), fake.tokens.Load())
	}

	*now = now.Add(61 * time.Second)
	if ok, _ := c.CanAccess(context.Background(), property, owner, Write); !ok {
		t.Fatal("access lost")
	}
	if fake.checks.Load() != 2 {
		t.Errorf("cache must expire after 60 s: checks %d", fake.checks.Load())
	}
}

func TestCanAccessDoesNotCacheRefusals(t *testing.T) {
	fake := &fakeCore{expires: 900, access: allowOwner(uuid.New())}
	c, _ := newClient(t, fake)
	stranger := uuid.New()

	for range 2 {
		ok, err := c.CanAccess(context.Background(), uuid.New(), stranger, Read)
		if err != nil || ok {
			t.Fatalf("CanAccess = %v, %v; want false, nil", ok, err)
		}
	}
	if fake.checks.Load() != 2 {
		t.Errorf("checks %d, want 2", fake.checks.Load())
	}
}

func TestServiceTokenIsRenewedBeforeExpiry(t *testing.T) {
	owner := uuid.New()
	fake := &fakeCore{expires: 60, access: allowOwner(owner)}
	c, now := newClient(t, fake)

	_, _ = c.CanAccess(context.Background(), uuid.New(), owner, Read)
	*now = now.Add(31 * time.Second) // 60 s lifetime minus the 30 s margin has passed
	_, _ = c.CanAccess(context.Background(), uuid.New(), owner, Read)

	if fake.tokens.Load() != 2 {
		t.Errorf("tokens %d, want 2", fake.tokens.Load())
	}
}

func TestRejectedTokenIsReplacedOnce(t *testing.T) {
	owner := uuid.New()
	fake := &fakeCore{expires: 900}
	fake.access = func(r *http.Request) int {
		if r.Header.Get("Authorization") == "Bearer token-1" {
			return http.StatusUnauthorized
		}
		return allowOwner(owner)(r)
	}
	c, _ := newClient(t, fake)

	ok, err := c.CanAccess(context.Background(), uuid.New(), owner, Write)

	if err != nil || !ok || fake.tokens.Load() != 2 {
		t.Errorf("CanAccess = %v, %v; tokens %d", ok, err, fake.tokens.Load())
	}
}

func TestUnexpectedAnswersAreUnavailable(t *testing.T) {
	cases := map[string]*fakeCore{
		"access check fails": {expires: 900, access: func(*http.Request) int { return http.StatusInternalServerError }},
		"token refused":      {expires: 900, tokenErr: http.StatusUnauthorized},
		"still unauthorized": {expires: 900, access: func(*http.Request) int { return http.StatusUnauthorized }},
	}
	for name, fake := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := newClient(t, fake)
			_, err := c.CanAccess(context.Background(), uuid.New(), uuid.New(), Read)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestUnreachableCoreIsUnavailable(t *testing.T) {
	c := New(config.Core{URL: "http://127.0.0.1:1", ClientID: "media-service", ClientSecret: "secret"})

	_, err := c.CanAccess(context.Background(), uuid.New(), uuid.New(), Read)

	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestUnreadableTokenResponseIsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>maintenance</html>"))
	}))
	t.Cleanup(server.Close)
	c := New(config.Core{URL: server.URL, ClientID: "media-service", ClientSecret: "secret"})

	if _, err := c.CanAccess(context.Background(), uuid.New(), uuid.New(), Read); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
