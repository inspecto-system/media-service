// Package httpapi contains the HTTP layer of the service: routing, middleware and error responses.
package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Code is a machine-readable error code of the API (platform/api/common.yaml, ErrorCode).
type Code string

// Error codes used by the service.
const (
	CodeValidation           Code = "VALIDATION_ERROR"
	CodeUnsupportedMediaType Code = "UNSUPPORTED_MEDIA_TYPE"
	CodeImageTooLarge        Code = "IMAGE_TOO_LARGE"
	CodeDimensionsTooLarge   Code = "IMAGE_DIMENSIONS_TOO_LARGE"
	CodeUnauthenticated      Code = "UNAUTHENTICATED"
	CodeTokenExpired         Code = "TOKEN_EXPIRED"
	CodeForbidden            Code = "FORBIDDEN"
	CodeNotFound             Code = "NOT_FOUND"
	CodeResourceInUse        Code = "RESOURCE_IN_USE"
	CodeDependencyDown       Code = "DEPENDENCY_UNAVAILABLE"
	CodeInternal             Code = "INTERNAL_ERROR"
	problemTypeBase               = "https://inspecto.dev/problems/"
	problemContentType            = "application/problem+json"
)

// FieldViolation is a single invalid field of a request.
type FieldViolation struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

// Problem is an RFC 9457 problem details body with the code and request id of the API.
type Problem struct {
	Type      string           `json:"type"`
	Title     string           `json:"title"`
	Status    int              `json:"status"`
	Code      Code             `json:"code"`
	Detail    string           `json:"detail,omitempty"`
	RequestID string           `json:"requestId"`
	Errors    []FieldViolation `json:"errors,omitempty"`
}

// WriteProblem writes an error response. The detail must not contain internal information such as
// stack traces or SQL (SECURITY.md §4).
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code Code, detail string) {
	writeProblem(w, r, Problem{Status: status, Code: code, Detail: detail})
}

// writeInvalid reports invalid request fields as 400 VALIDATION_ERROR.
func writeInvalid(w http.ResponseWriter, r *http.Request, violations ...FieldViolation) {
	writeProblem(w, r, Problem{
		Status: http.StatusBadRequest, Code: CodeValidation,
		Detail: "Request contains invalid fields", Errors: violations,
	})
}

func writeProblem(w http.ResponseWriter, r *http.Request, problem Problem) {
	problem.Type = problemTypeBase + strings.ReplaceAll(strings.ToLower(string(problem.Code)), "_", "-")
	problem.Title = http.StatusText(problem.Status)
	problem.RequestID = RequestIDFrom(r.Context())
	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(problem.Status)
	// The status is already sent; a failed write means the client has gone and nothing is left to do.
	_ = json.NewEncoder(w).Encode(problem)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status is already sent; a failed write means the client has gone and nothing is left to do.
	_ = json.NewEncoder(w).Encode(body)
}
