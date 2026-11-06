package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/auth"
	"github.com/inspecto-system/media-service/internal/imaging"
	"github.com/inspecto-system/media-service/internal/service"
	"github.com/inspecto-system/media-service/internal/storage"
)

// ServiceVerifier checks service tokens.
type ServiceVerifier interface {
	VerifyService(token string) (auth.Service, error)
}

// InternalService is the business layer behind /internal/v1/photos.
type InternalService interface {
	Metadata(ctx context.Context, photoID uuid.UUID) (storage.Photo, error)
	Attach(ctx context.Context, photoID uuid.UUID) error
	Content(ctx context.Context, photoID uuid.UUID, variant string) (io.ReadCloser, int64, error)
	Delete(ctx context.Context, photoID uuid.UUID) error
	DeleteProperty(ctx context.Context, propertyID uuid.UUID) error
	DeleteOwner(ctx context.Context, ownerID uuid.UUID) error
}

// serviceAuthenticated admits only service tokens with the given scope (SECURITY.md §8).
func serviceAuthenticated(verifier ServiceVerifier, scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				unauthenticated(w, r, CodeUnauthenticated)
				return
			}
			caller, err := verifier.VerifyService(token)
			if errors.Is(err, auth.ErrExpiredToken) {
				unauthenticated(w, r, CodeTokenExpired)
				return
			}
			if err != nil {
				unauthenticated(w, r, CodeUnauthenticated)
				return
			}
			if !caller.Has(scope) {
				WriteProblem(w, r, http.StatusForbidden, CodeForbidden, "The service token lacks the scope "+scope)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type internalHandlers struct {
	photos InternalService
	logger *slog.Logger
}

type metadataResponse struct {
	photoResponse
	OwnerID uuid.UUID `json:"ownerId"`
	Status  string    `json:"status"`
	SHA256  string    `json:"sha256"`
}

func (h internalHandlers) metadata(w http.ResponseWriter, r *http.Request) {
	photoID, ok := photoIDParam(w, r)
	if !ok {
		return
	}
	photo, err := h.photos.Metadata(r.Context(), photoID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, metadataResponse{
		photoResponse: toPhotoResponse(photo),
		OwnerID:       photo.OwnerID,
		Status:        photo.Status,
		SHA256:        photo.SHA256,
	})
}

func (h internalHandlers) attach(w http.ResponseWriter, r *http.Request) {
	photoID, ok := photoIDParam(w, r)
	if !ok {
		return
	}
	if err := h.photos.Attach(r.Context(), photoID); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h internalHandlers) content(w http.ResponseWriter, r *http.Request) {
	photoID, ok := photoIDParam(w, r)
	if !ok {
		return
	}
	variant, ok := variantParam(w, r)
	if !ok {
		return
	}
	body, size, err := h.photos.Content(r.Context(), photoID, variant)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeImage(w, body, size)
}

func (h internalHandlers) delete(w http.ResponseWriter, r *http.Request) {
	photoID, ok := photoIDParam(w, r)
	if !ok {
		return
	}
	if err := h.photos.Delete(r.Context(), photoID); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteInBulk removes all photos of exactly one property or one owner.
func (h internalHandlers) deleteInBulk(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	property, owner := query.Get("propertyId"), query.Get("ownerId")
	if (property == "") == (owner == "") {
		writeInvalid(w, r, FieldViolation{Field: "propertyId", Code: "EXACTLY_ONE_OF_PROPERTY_OR_OWNER"})
		return
	}
	field, raw, remove := "propertyId", property, h.photos.DeleteProperty
	if owner != "" {
		field, raw, remove = "ownerId", owner, h.photos.DeleteOwner
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeInvalid(w, r, FieldViolation{Field: field, Code: "INVALID_FORMAT"})
		return
	}
	if err := remove(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h internalHandlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	photoHandlers{logger: h.logger}.fail(w, r, err)
}

func toPhotoResponse(photo storage.Photo) photoResponse {
	return photoResponse{
		ID: photo.ID, PropertyID: photo.PropertyID, ContentType: photo.ContentType,
		Width: photo.Width, Height: photo.Height, SizeBytes: photo.SizeBytes, CreatedAt: photo.CreatedAt,
	}
}

// variantParam reads ?variant, preview by default (common.yaml, PhotoVariant).
func variantParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	switch variant := r.URL.Query().Get("variant"); variant {
	case "":
		return service.VariantPreview, true
	case service.VariantOriginal, service.VariantPreview, service.VariantThumb:
		return variant, true
	default:
		writeInvalid(w, r, FieldViolation{Field: "variant", Code: "INVALID_FORMAT"})
		return "", false
	}
}

// writeImage sends a stored JPEG; it is never cached by shared caches.
func writeImage(w http.ResponseWriter, body io.ReadCloser, size int64) {
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Type", string(imaging.JPEG))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Cache-Control", "private")
	w.WriteHeader(http.StatusOK)
	// The status is already sent; a failed copy means the client has gone.
	_, _ = io.Copy(w, body)
}
