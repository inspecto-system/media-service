package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/auth"
	"github.com/inspecto-system/media-service/internal/imaging"
	"github.com/inspecto-system/media-service/internal/service"
	"github.com/inspecto-system/media-service/internal/storage"
)

var inspector = auth.User{ID: uuid.New(), Role: auth.RoleInspector}

// tokens maps test tokens to their outcome.
type tokens map[string]struct {
	user auth.User
	err  error
}

func (t tokens) Verify(token string) (auth.User, error) {
	outcome, ok := t[token]
	if !ok {
		return auth.User{}, auth.ErrInvalidToken
	}
	return outcome.user, outcome.err
}

// photoService records calls and returns prepared results.
type photoService struct {
	uploaded   service.Upload
	uploadedBy uuid.UUID
	err        error
	variant    string
}

func (p *photoService) Upload(_ context.Context, userID uuid.UUID, in service.Upload) (storage.Photo, error) {
	p.uploaded, p.uploadedBy = in, userID
	if p.err != nil {
		return storage.Photo{}, p.err
	}
	return storage.Photo{
		ID: uuid.New(), PropertyID: in.PropertyID, ContentType: "image/jpeg", Width: 40, Height: 30,
		SizeBytes: 1234, CreatedAt: time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC),
	}, nil
}

func (p *photoService) Content(_ context.Context, _, _ uuid.UUID, variant string) (io.ReadCloser, int64, error) {
	p.variant = variant
	if p.err != nil {
		return nil, 0, p.err
	}
	return io.NopCloser(strings.NewReader("jpeg")), 4, nil
}

func (p *photoService) Delete(context.Context, uuid.UUID, uuid.UUID) error {
	return p.err
}

const maxFile = 1 << 10

func photoRouter(svc *photoService) http.Handler {
	return NewRouter(Dependencies{
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Verifier: tokens{
			"good":    {user: inspector},
			"expired": {err: auth.ErrExpiredToken},
			"client":  {user: auth.User{ID: uuid.New(), Role: "CLIENT"}},
		},
		Photos: svc,
		Limits: Limits{MaxFileSizeBytes: maxFile, MaxPixels: 1000, MaxPhotosPerObservation: 10},
	})
}

func multipartBody(t *testing.T, fields map[string]string, file []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := w.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if file != nil {
		part, err := w.CreateFormFile("file", "photo.jpg")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(file)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, w.FormDataContentType()
}

func call(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func request(method, path, token string, body io.Reader) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) Problem {
	t.Helper()
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem: %v (%s)", err, rec.Body.String())
	}
	return p
}

func TestPhotoEndpointsRequireValidToken(t *testing.T) {
	router := photoRouter(&photoService{})
	cases := map[string]struct {
		header string
		status int
		code   Code
	}{
		"no header":     {"", http.StatusUnauthorized, CodeUnauthenticated},
		"not bearer":    {"Basic abc", http.StatusUnauthorized, CodeUnauthenticated},
		"unknown token": {"Bearer forged", http.StatusUnauthorized, CodeUnauthenticated},
		"expired":       {"Bearer expired", http.StatusUnauthorized, CodeTokenExpired},
		"client role":   {"Bearer client", http.StatusForbidden, CodeForbidden},
	}
	paths := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/photos/limits"},
		{http.MethodPost, "/api/v1/photos"},
		{http.MethodGet, "/api/v1/photos/" + uuid.NewString() + "/content"},
		{http.MethodDelete, "/api/v1/photos/" + uuid.NewString()},
	}
	for name, tc := range cases {
		for _, p := range paths {
			req := request(p.method, p.path, "", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := call(router, req)
			if rec.Code != tc.status || problemOf(t, rec).Code != tc.code {
				t.Errorf("%s %s %s: %d %s", name, p.method, p.path, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestLimitsArePublished(t *testing.T) {
	rec := call(photoRouter(&photoService{}), request(http.MethodGet, "/api/v1/photos/limits", "good", nil))

	var body limitsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || body.MaxFileSizeBytes != maxFile || body.MaxPixels != 1000 ||
		body.MaxPhotosPerObservation != 10 || len(body.AllowedContentTypes) != 3 {
		t.Errorf("got %d %+v", rec.Code, body)
	}
}

func TestUploadPassesFileOwnerAndKey(t *testing.T) {
	svc := &photoService{}
	property := uuid.New()
	body, contentType := multipartBody(t, map[string]string{"propertyId": property.String(), "extra": "x"}, []byte("img"))
	req := request(http.MethodPost, "/api/v1/photos", "good", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Idempotency-Key", "retry-key-1")

	rec := call(photoRouter(svc), req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var photo map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &photo); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"propertyId": property.String(), "contentType": "image/jpeg",
		"createdAt": "2026-11-05T09:00:00Z", "sizeBytes": float64(1234),
	} {
		if photo[key] != want {
			t.Errorf("%s = %v, want %v", key, photo[key], want)
		}
	}
	type call struct {
		by, property uuid.UUID
		data, key    string
	}
	got := call{svc.uploadedBy, svc.uploaded.PropertyID, string(svc.uploaded.Data), svc.uploaded.IdempotencyKey}
	if want := (call{inspector.ID, property, "img", "retry-key-1"}); got != want {
		t.Errorf("service got %+v, want %+v", got, want)
	}
}

func TestUploadValidatesRequest(t *testing.T) {
	cases := map[string]struct {
		fields map[string]string
		file   []byte
		raw    string
		key    string
		field  string
		code   string
	}{
		"no file":           {fields: map[string]string{"propertyId": uuid.NewString()}, field: "file", code: "NOT_BLANK"},
		"empty file":        {fields: map[string]string{"propertyId": uuid.NewString()}, file: []byte{}, field: "file", code: "NOT_BLANK"},
		"no property":       {file: []byte("img"), field: "propertyId", code: "NOT_BLANK"},
		"bad property":      {fields: map[string]string{"propertyId": "42"}, file: []byte("img"), field: "propertyId", code: "INVALID_FORMAT"},
		"short idempotency": {fields: map[string]string{"propertyId": uuid.NewString()}, file: []byte("img"), key: "short", field: "Idempotency-Key", code: "INVALID_LENGTH"},
		"not multipart":     {raw: "{}", field: "file", code: "NOT_BLANK"},
		"broken multipart":  {raw: "--boundary\r\nnonsense", field: "file", code: "INVALID"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var req *http.Request
			switch {
			case tc.raw == "{}":
				req = request(http.MethodPost, "/api/v1/photos", "good", strings.NewReader(tc.raw))
				req.Header.Set("Content-Type", "application/json")
			case tc.raw != "":
				req = request(http.MethodPost, "/api/v1/photos", "good", strings.NewReader(tc.raw))
				req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
			default:
				body, contentType := multipartBody(t, tc.fields, tc.file)
				req = request(http.MethodPost, "/api/v1/photos", "good", body)
				req.Header.Set("Content-Type", contentType)
			}
			if tc.key != "" {
				req.Header.Set("Idempotency-Key", tc.key)
			}

			rec := call(photoRouter(&photoService{}), req)

			p := problemOf(t, rec)
			if rec.Code != http.StatusBadRequest || p.Code != CodeValidation || len(p.Errors) == 0 ||
				p.Errors[0].Field != tc.field || p.Errors[0].Code != tc.code {
				t.Errorf("got %d %+v", rec.Code, p)
			}
		})
	}
}

func TestUploadRejectsTooLargeFileBeforeProcessing(t *testing.T) {
	svc := &photoService{}
	body, contentType := multipartBody(t, map[string]string{"propertyId": uuid.NewString()}, bytes.Repeat([]byte("x"), maxFile+1))
	req := request(http.MethodPost, "/api/v1/photos", "good", body)
	req.Header.Set("Content-Type", contentType)

	rec := call(photoRouter(svc), req)

	if rec.Code != http.StatusRequestEntityTooLarge || problemOf(t, rec).Code != CodeImageTooLarge {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
	if svc.uploaded.Data != nil {
		t.Error("service was called for an oversized file")
	}
}

func TestUploadRejectsOversizedBody(t *testing.T) {
	huge := bytes.Repeat([]byte("y"), maxFile+multipartOverhead+1)
	body, contentType := multipartBody(t, map[string]string{"propertyId": uuid.NewString(), "padding": string(huge)}, []byte("img"))
	req := request(http.MethodPost, "/api/v1/photos", "good", body)
	req.Header.Set("Content-Type", contentType)

	rec := call(photoRouter(&photoService{}), req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestServiceErrorsMapToProblems(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		code   Code
	}{
		"unsupported": {imaging.ErrUnsupportedType, http.StatusBadRequest, CodeUnsupportedMediaType},
		"too many px": {imaging.ErrTooManyPixels, http.StatusBadRequest, CodeDimensionsTooLarge},
		"no access":   {service.ErrNotFound, http.StatusNotFound, CodeNotFound},
		"attached":    {service.ErrAttached, http.StatusConflict, CodeResourceInUse},
		"core down":   {service.ErrUnavailable, http.StatusServiceUnavailable, CodeDependencyDown},
		"unexpected":  {errors.New("boom"), http.StatusInternalServerError, CodeInternal},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			router := photoRouter(&photoService{err: tc.err})
			body, contentType := multipartBody(t, map[string]string{"propertyId": uuid.NewString()}, []byte("img"))
			upload := request(http.MethodPost, "/api/v1/photos", "good", body)
			upload.Header.Set("Content-Type", contentType)
			for _, req := range []*http.Request{
				upload,
				request(http.MethodGet, "/api/v1/photos/"+uuid.NewString()+"/content", "good", nil),
				request(http.MethodDelete, "/api/v1/photos/"+uuid.NewString(), "good", nil),
			} {
				rec := call(router, req)
				if rec.Code != tc.status || problemOf(t, rec).Code != tc.code {
					t.Errorf("%s %s: %d %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "boom") {
					t.Errorf("internal error text leaked: %s", rec.Body.String())
				}
			}
		})
	}
}

func TestContentServesJPEGPrivately(t *testing.T) {
	svc := &photoService{}
	router := photoRouter(svc)

	rec := call(router, request(http.MethodGet, "/api/v1/photos/"+uuid.NewString()+"/content", "good", nil))

	if rec.Code != http.StatusOK || rec.Body.String() != "jpeg" || svc.variant != service.VariantPreview {
		t.Fatalf("got %d %q variant %q", rec.Code, rec.Body.String(), svc.variant)
	}
	for header, want := range map[string]string{
		"Content-Type": "image/jpeg", "Content-Length": "4", "Cache-Control": "private",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	for _, variant := range []string{service.VariantOriginal, service.VariantThumb} {
		call(router, request(http.MethodGet, "/api/v1/photos/"+uuid.NewString()+"/content?variant="+variant, "good", nil))
		if svc.variant != variant {
			t.Errorf("variant %q passed as %q", variant, svc.variant)
		}
	}
}

func TestContentAndDeleteValidateParameters(t *testing.T) {
	router := photoRouter(&photoService{})
	cases := []struct {
		req   *http.Request
		field string
	}{
		{request(http.MethodGet, "/api/v1/photos/not-a-uuid/content", "good", nil), "photoId"},
		{request(http.MethodGet, "/api/v1/photos/"+uuid.NewString()+"/content?variant=huge", "good", nil), "variant"},
		{request(http.MethodDelete, "/api/v1/photos/not-a-uuid", "good", nil), "photoId"},
	}
	for _, tc := range cases {
		rec := call(router, tc.req)
		p := problemOf(t, rec)
		if rec.Code != http.StatusBadRequest || len(p.Errors) != 1 || p.Errors[0].Field != tc.field {
			t.Errorf("%s: %d %+v", tc.req.URL, rec.Code, p)
		}
	}
}

func TestDeleteAnswersNoContent(t *testing.T) {
	rec := call(photoRouter(&photoService{}), request(http.MethodDelete, "/api/v1/photos/"+uuid.NewString(), "good", nil))

	if rec.Code != http.StatusNoContent {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}
