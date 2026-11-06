package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/auth"
	"github.com/inspecto-system/media-service/internal/service"
	"github.com/inspecto-system/media-service/internal/storage"
)

// serviceTokens maps test tokens to the calling service.
type serviceTokens map[string]auth.Service

func (s serviceTokens) VerifyService(token string) (auth.Service, error) {
	if token == "expired" {
		return auth.Service{}, auth.ErrExpiredToken
	}
	caller, ok := s[token]
	if !ok {
		return auth.Service{}, auth.ErrInvalidToken
	}
	return caller, nil
}

// internalService records the last call.
type internalService struct {
	err   error
	calls []string
}

func (s *internalService) record(call string) error {
	s.calls = append(s.calls, call)
	return s.err
}

func (s *internalService) Metadata(_ context.Context, id uuid.UUID) (storage.Photo, error) {
	if err := s.record("metadata " + id.String()); err != nil {
		return storage.Photo{}, err
	}
	return storage.Photo{
		ID: id, OwnerID: id, PropertyID: id, Status: storage.StatusUploaded, ContentType: "image/jpeg",
		Width: 4, Height: 3, SizeBytes: 9, SHA256: strings.Repeat("c", 64), CreatedAt: time.Unix(0, 0).UTC(),
	}, nil
}

func (s *internalService) Attach(_ context.Context, id uuid.UUID) error {
	return s.record("attach " + id.String())
}

func (s *internalService) Content(_ context.Context, id uuid.UUID, variant string) (io.ReadCloser, int64, error) {
	if err := s.record("content " + id.String() + " " + variant); err != nil {
		return nil, 0, err
	}
	return io.NopCloser(strings.NewReader("jpg")), 3, nil
}

func (s *internalService) Delete(_ context.Context, id uuid.UUID) error {
	return s.record("delete " + id.String())
}

func (s *internalService) DeleteProperty(_ context.Context, id uuid.UUID) error {
	return s.record("delete property " + id.String())
}

func (s *internalService) DeleteOwner(_ context.Context, id uuid.UUID) error {
	return s.record("delete owner " + id.String())
}

func internalRouter(svc *internalService) http.Handler {
	return NewRouter(Dependencies{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Services: serviceTokens{
			"core":   {Name: "core-service", Scopes: []string{auth.ScopePhotosManage}},
			"report": {Name: "report-service", Scopes: []string{auth.ScopePhotosRead}},
		},
		Internal: svc,
	})
}

func TestInternalEndpointsRequireServiceTokenWithScope(t *testing.T) {
	id := uuid.NewString()
	manage := []struct{ method, path string }{
		{http.MethodGet, "/internal/v1/photos/" + id},
		{http.MethodPost, "/internal/v1/photos/" + id + "/attach"},
		{http.MethodDelete, "/internal/v1/photos/" + id},
		{http.MethodDelete, "/internal/v1/photos?propertyId=" + id},
	}
	router := internalRouter(&internalService{})
	cases := map[string]struct {
		token  string
		status int
		code   Code
	}{
		"no token":   {"", http.StatusUnauthorized, CodeUnauthenticated},
		"user token": {"user-token", http.StatusUnauthorized, CodeUnauthenticated},
		"expired":    {"expired", http.StatusUnauthorized, CodeTokenExpired},
		"read scope": {"report", http.StatusForbidden, CodeForbidden},
	}
	for name, tc := range cases {
		for _, e := range manage {
			rec := call(router, request(e.method, e.path, tc.token, nil))
			if rec.Code != tc.status || problemOf(t, rec).Code != tc.code {
				t.Errorf("%s %s %s: %d %s", name, e.method, e.path, rec.Code, rec.Body.String())
			}
		}
	}
	if rec := call(router, request(http.MethodGet, "/internal/v1/photos/"+id+"/content", "core", nil)); rec.Code != http.StatusForbidden {
		t.Errorf("content with manage scope: %d", rec.Code)
	}
}

func TestInternalMetadata(t *testing.T) {
	id := uuid.New()

	rec := call(internalRouter(&internalService{}), request(http.MethodGet, "/internal/v1/photos/"+id.String(), "core", nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"id": id.String(), "ownerId": id.String(), "propertyId": id.String(), "status": "UPLOADED",
		"sha256": strings.Repeat("c", 64), "contentType": "image/jpeg", "width": float64(4),
	} {
		if body[key] != want {
			t.Errorf("%s = %v, want %v", key, body[key], want)
		}
	}
}

func TestInternalCallsReachTheService(t *testing.T) {
	id := uuid.NewString()
	cases := []struct {
		method, path, token string
		status              int
		call                string
	}{
		{http.MethodPost, "/internal/v1/photos/" + id + "/attach", "core", http.StatusNoContent, "attach " + id},
		{http.MethodDelete, "/internal/v1/photos/" + id, "core", http.StatusNoContent, "delete " + id},
		{http.MethodDelete, "/internal/v1/photos?propertyId=" + id, "core", http.StatusNoContent, "delete property " + id},
		{http.MethodDelete, "/internal/v1/photos?ownerId=" + id, "core", http.StatusNoContent, "delete owner " + id},
		{http.MethodGet, "/internal/v1/photos/" + id + "/content?variant=original", "report", http.StatusOK, "content " + id + " original"},
		{http.MethodGet, "/internal/v1/photos/" + id + "/content", "report", http.StatusOK, "content " + id + " preview"},
	}
	for _, tc := range cases {
		svc := &internalService{}
		rec := call(internalRouter(svc), request(tc.method, tc.path, tc.token, nil))
		if rec.Code != tc.status || len(svc.calls) != 1 || svc.calls[0] != tc.call {
			t.Errorf("%s %s: %d, calls %v", tc.method, tc.path, rec.Code, svc.calls)
		}
	}
}

func TestBulkDeleteNeedsExactlyOneValidFilter(t *testing.T) {
	id := uuid.NewString()
	cases := map[string]struct{ query, field string }{
		"none":         {"", "propertyId"},
		"both":         {"?propertyId=" + id + "&ownerId=" + id, "propertyId"},
		"bad property": {"?propertyId=1", "propertyId"},
		"bad owner":    {"?ownerId=x", "ownerId"},
	}
	for name, tc := range cases {
		svc := &internalService{}
		rec := call(internalRouter(svc), request(http.MethodDelete, "/internal/v1/photos"+tc.query, "core", nil))
		p := problemOf(t, rec)
		if rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != tc.field || len(svc.calls) != 0 {
			t.Errorf("%s: %d %+v, calls %v", name, rec.Code, p, svc.calls)
		}
	}
}

func TestInternalErrorsAndParameters(t *testing.T) {
	id := uuid.NewString()
	cases := []struct {
		svc    *internalService
		method string
		path   string
		token  string
		status int
	}{
		{&internalService{err: service.ErrNotFound}, http.MethodPost, "/internal/v1/photos/" + id + "/attach", "core", http.StatusNotFound},
		{&internalService{err: service.ErrNotFound}, http.MethodGet, "/internal/v1/photos/" + id, "core", http.StatusNotFound},
		{&internalService{err: service.ErrUnavailable}, http.MethodDelete, "/internal/v1/photos/" + id, "core", http.StatusServiceUnavailable},
		{&internalService{err: service.ErrUnavailable}, http.MethodDelete, "/internal/v1/photos?ownerId=" + id, "core", http.StatusServiceUnavailable},
		{&internalService{err: service.ErrNotFound}, http.MethodGet, "/internal/v1/photos/" + id + "/content", "report", http.StatusNotFound},
		{&internalService{}, http.MethodGet, "/internal/v1/photos/" + id + "/content?variant=raw", "report", http.StatusBadRequest},
		{&internalService{}, http.MethodGet, "/internal/v1/photos/nope", "core", http.StatusBadRequest},
		{&internalService{}, http.MethodPost, "/internal/v1/photos/nope/attach", "core", http.StatusBadRequest},
		{&internalService{}, http.MethodDelete, "/internal/v1/photos/nope", "core", http.StatusBadRequest},
		{&internalService{}, http.MethodGet, "/internal/v1/photos/nope/content", "report", http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := call(internalRouter(tc.svc), request(tc.method, tc.path, tc.token, nil))
		if rec.Code != tc.status {
			t.Errorf("%s %s: %d, want %d (%s)", tc.method, tc.path, rec.Code, tc.status, rec.Body.String())
		}
		if problemOf(t, rec).Code == "" {
			t.Errorf("%s %s: no problem code", tc.method, tc.path)
		}
	}
}
