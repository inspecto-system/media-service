package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/imaging"
	"github.com/inspecto-system/media-service/internal/service"
	"github.com/inspecto-system/media-service/internal/storage"
)

// multipartOverhead is the room for multipart headers and the propertyId field on top of the file.
const multipartOverhead = 64 << 10

// PhotoService is the business layer behind the photo endpoints.
type PhotoService interface {
	Upload(ctx context.Context, userID uuid.UUID, in service.Upload) (storage.Photo, error)
	Content(ctx context.Context, userID, photoID uuid.UUID, variant string) (io.ReadCloser, int64, error)
	Delete(ctx context.Context, userID, photoID uuid.UUID) error
}

// Limits are the upload limits published by GET /photos/limits.
type Limits struct {
	MaxFileSizeBytes        int64
	MaxPixels               int64
	MaxPhotosPerObservation int
}

type photoHandlers struct {
	photos PhotoService
	limits Limits
	logger *slog.Logger
}

type limitsResponse struct {
	AllowedContentTypes     []imaging.Format `json:"allowedContentTypes"`
	MaxFileSizeBytes        int64            `json:"maxFileSizeBytes"`
	MaxPixels               int64            `json:"maxPixels"`
	MaxPhotosPerObservation int              `json:"maxPhotosPerObservation"`
}

type photoResponse struct {
	ID          uuid.UUID `json:"id"`
	PropertyID  uuid.UUID `json:"propertyId"`
	ContentType string    `json:"contentType"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	SizeBytes   int64     `json:"sizeBytes"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (h photoHandlers) getLimits(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, limitsResponse{
		AllowedContentTypes:     imaging.Formats,
		MaxFileSizeBytes:        h.limits.MaxFileSizeBytes,
		MaxPixels:               h.limits.MaxPixels,
		MaxPhotosPerObservation: h.limits.MaxPhotosPerObservation,
	})
}

func (h photoHandlers) upload(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key != "" && (len(key) < 8 || len(key) > 64) {
		writeInvalid(w, r, FieldViolation{Field: "Idempotency-Key", Code: "INVALID_LENGTH"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.limits.MaxFileSizeBytes+multipartOverhead)
	form, err := h.readUploadForm(r)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge) || errors.Is(err, errFileTooLarge):
		WriteProblem(w, r, http.StatusRequestEntityTooLarge, CodeImageTooLarge, "The file is larger than allowed")
		return
	case errors.Is(err, errNotMultipart):
		writeInvalid(w, r, FieldViolation{Field: "file", Code: "NOT_BLANK"})
		return
	case err != nil:
		writeInvalid(w, r, FieldViolation{Field: "file", Code: "INVALID"})
		return
	}
	if violations := form.violations(); len(violations) > 0 {
		writeInvalid(w, r, violations...)
		return
	}
	propertyID := uuid.MustParse(form.propertyID)
	photo, err := h.photos.Upload(r.Context(), userFrom(r.Context()).ID,
		service.Upload{PropertyID: propertyID, IdempotencyKey: key, Data: form.file})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, photoResponse{
		ID: photo.ID, PropertyID: photo.PropertyID, ContentType: photo.ContentType,
		Width: photo.Width, Height: photo.Height, SizeBytes: photo.SizeBytes, CreatedAt: photo.CreatedAt,
	})
}

func (h photoHandlers) content(w http.ResponseWriter, r *http.Request) {
	photoID, ok := photoIDParam(w, r)
	if !ok {
		return
	}
	variant := r.URL.Query().Get("variant")
	switch variant {
	case "":
		variant = service.VariantPreview
	case service.VariantOriginal, service.VariantPreview, service.VariantThumb:
	default:
		writeInvalid(w, r, FieldViolation{Field: "variant", Code: "INVALID_FORMAT"})
		return
	}
	body, size, err := h.photos.Content(r.Context(), userFrom(r.Context()).ID, photoID, variant)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() { _ = body.Close() }()
	w.Header().Set("Content-Type", string(imaging.JPEG))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Cache-Control", "private")
	w.WriteHeader(http.StatusOK)
	// The status is already sent; a failed copy means the client has gone.
	_, _ = io.Copy(w, body)
}

func (h photoHandlers) delete(w http.ResponseWriter, r *http.Request) {
	photoID, ok := photoIDParam(w, r)
	if !ok {
		return
	}
	if err := h.photos.Delete(r.Context(), userFrom(r.Context()).ID, photoID); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func photoIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "photoId"))
	if err != nil {
		writeInvalid(w, r, FieldViolation{Field: "photoId", Code: "INVALID_FORMAT"})
		return uuid.Nil, false
	}
	return id, true
}

func (h photoHandlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, imaging.ErrUnsupportedType):
		WriteProblem(w, r, http.StatusBadRequest, CodeUnsupportedMediaType, "Only JPEG, PNG and WebP images are accepted")
	case errors.Is(err, imaging.ErrTooManyPixels):
		WriteProblem(w, r, http.StatusBadRequest, CodeDimensionsTooLarge, "The image has more pixels than allowed")
	case errors.Is(err, service.ErrNotFound):
		WriteProblem(w, r, http.StatusNotFound, CodeNotFound, "Resource not found")
	case errors.Is(err, service.ErrAttached):
		WriteProblem(w, r, http.StatusConflict, CodeResourceInUse, "The photo is attached to an observation")
	case errors.Is(err, service.ErrUnavailable):
		h.logger.ErrorContext(r.Context(), "photo request failed", slog.Any("error", err))
		WriteProblem(w, r, http.StatusServiceUnavailable, CodeDependencyDown, "A dependency is unavailable")
	default:
		h.logger.ErrorContext(r.Context(), "photo request failed", slog.Any("error", err))
		WriteProblem(w, r, http.StatusInternalServerError, CodeInternal, "Unexpected error")
	}
}

var (
	errNotMultipart = errors.New("not a multipart request")
	errFileTooLarge = errors.New("file too large")
)

type uploadForm struct {
	file       []byte
	hasFile    bool
	propertyID string
}

// readUploadForm streams the multipart body; the file is read into memory up to the limit.
func (h photoHandlers) readUploadForm(r *http.Request) (uploadForm, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return uploadForm{}, errNotMultipart
	}
	var form uploadForm
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return form, nil
		}
		if err != nil {
			return uploadForm{}, err
		}
		if err := h.readPart(part, &form); err != nil {
			return uploadForm{}, err
		}
	}
}

func (h photoHandlers) readPart(part *multipart.Part, form *uploadForm) error {
	defer func() { _ = part.Close() }()
	switch part.FormName() {
	case "file":
		data, err := io.ReadAll(io.LimitReader(part, h.limits.MaxFileSizeBytes+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > h.limits.MaxFileSizeBytes {
			return errFileTooLarge
		}
		form.file, form.hasFile = data, true
	case "propertyId":
		data, err := io.ReadAll(io.LimitReader(part, 64))
		if err != nil {
			return err
		}
		form.propertyID = string(data)
	default:
		// Unknown parts are ignored; the body size limit still applies to them.
		_, err := io.Copy(io.Discard, part)
		return err
	}
	return nil
}

func (f uploadForm) violations() []FieldViolation {
	var violations []FieldViolation
	if !f.hasFile || len(f.file) == 0 {
		violations = append(violations, FieldViolation{Field: "file", Code: "NOT_BLANK"})
	}
	switch _, err := uuid.Parse(f.propertyID); {
	case f.propertyID == "":
		violations = append(violations, FieldViolation{Field: "propertyId", Code: "NOT_BLANK"})
	case err != nil:
		violations = append(violations, FieldViolation{Field: "propertyId", Code: "INVALID_FORMAT"})
	}
	return violations
}
