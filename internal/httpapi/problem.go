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
	CodeNotFound       Code = "NOT_FOUND"
	CodeValidation     Code = "VALIDATION_ERROR"
	CodeInternal       Code = "INTERNAL_ERROR"
	problemTypeBase         = "https://inspecto.dev/problems/"
	problemContentType      = "application/problem+json"
)

// Problem is an RFC 9457 problem details body with the code and request id of the API.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      Code   `json:"code"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"requestId"`
}

// WriteProblem writes an error response. The detail must not contain internal information such as
// stack traces or SQL (SECURITY.md §4).
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code Code, detail string) {
	problem := Problem{
		Type:      problemTypeBase + strings.ReplaceAll(strings.ToLower(string(code)), "_", "-"),
		Title:     http.StatusText(status),
		Status:    status,
		Code:      code,
		Detail:    detail,
		RequestID: RequestIDFrom(r.Context()),
	}
	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(status)
	// The status is already sent; a failed write means the client has gone and nothing is left to do.
	_ = json.NewEncoder(w).Encode(problem)
}
