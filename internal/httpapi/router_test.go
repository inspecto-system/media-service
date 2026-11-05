package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type pinger struct{ err error }

func (p pinger) Ping(context.Context) error { return p.err }

func newTestRouter(database Pinger) (http.Handler, *bytes.Buffer) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	return NewRouter(Dependencies{Logger: logger, Ready: []Pinger{database}}), &logs
}

func serve(handler http.Handler, method, path string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	for key, values := range header {
		req.Header[key] = values
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
	}
}

func TestHealthLiveIsUp(t *testing.T) {
	router, _ := newTestRouter(pinger{})

	rec := serve(router, http.MethodGet, "/health/live", nil)

	var body map[string]string
	decode(t, rec, &body)
	if rec.Code != http.StatusOK || body["status"] != "UP" {
		t.Errorf("got %d %v", rec.Code, body)
	}
}

func TestHealthReadyFollowsDatabase(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		state  string
	}{
		"database answers": {nil, http.StatusOK, "UP"},
		"database down":    {errors.New("connection refused"), http.StatusServiceUnavailable, "DOWN"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			router, logs := newTestRouter(pinger{err: tc.err})

			rec := serve(router, http.MethodGet, "/health/ready", nil)

			var body map[string]string
			decode(t, rec, &body)
			if rec.Code != tc.status || body["status"] != tc.state {
				t.Errorf("got %d %v, want %d %s", rec.Code, body, tc.status, tc.state)
			}
			if strings.Contains(rec.Body.String(), "refused") {
				t.Errorf("cause leaked to the client: %s", rec.Body.String())
			}
			if tc.err != nil && !strings.Contains(logs.String(), "connection refused") {
				t.Errorf("cause not logged: %s", logs.String())
			}
		})
	}
}

func TestUnknownPathIsProblemWithRequestID(t *testing.T) {
	router, _ := newTestRouter(pinger{})

	rec := serve(router, http.MethodGet, "/nowhere", http.Header{RequestIDHeader: {"abc-123"}})

	var problem Problem
	decode(t, rec, &problem)
	if rec.Code != http.StatusNotFound || problem.Code != CodeNotFound {
		t.Errorf("got %d %+v", rec.Code, problem)
	}
	if problem.Type != "https://inspecto.dev/problems/not-found" || problem.Title != "Not Found" {
		t.Errorf("unexpected type/title %+v", problem)
	}
	if problem.RequestID != "abc-123" || rec.Header().Get(RequestIDHeader) != "abc-123" {
		t.Errorf("request id not passed through: %q / %q", problem.RequestID, rec.Header().Get(RequestIDHeader))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestWrongMethodIsValidationProblem(t *testing.T) {
	router, _ := newTestRouter(pinger{})

	rec := serve(router, http.MethodPost, "/health/live", nil)

	var problem Problem
	decode(t, rec, &problem)
	if rec.Code != http.StatusMethodNotAllowed || problem.Code != CodeValidation {
		t.Errorf("got %d %+v", rec.Code, problem)
	}
}

func TestInvalidRequestIDIsReplaced(t *testing.T) {
	router, _ := newTestRouter(pinger{})
	invalid := []string{"", "has space", strings.Repeat("a", 65), "semi;colon"}

	for _, id := range invalid {
		rec := serve(router, http.MethodGet, "/health/live", http.Header{RequestIDHeader: {id}})

		got := rec.Header().Get(RequestIDHeader)
		if got == id || !validRequestID.MatchString(got) {
			t.Errorf("incoming %q: response id %q", id, got)
		}
	}
}

func TestAccessLogHasRequestDataButNoQuery(t *testing.T) {
	router, logs := newTestRouter(pinger{})

	serve(router, http.MethodGet, "/health/live?email=user@example.com", http.Header{RequestIDHeader: {"req-1"}})

	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("log is not JSON: %v", err)
	}
	if record["url.path"] != "/health/live" || record["http.response.status_code"] != float64(200) ||
		record["trace.id"] != "req-1" || record["http.request.method"] != "GET" {
		t.Errorf("unexpected access log %v", record)
	}
	if strings.Contains(logs.String(), "example.com") {
		t.Errorf("query string logged: %s", logs.String())
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := withRequestID(withRecovery(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})))

	rec := serve(handler, http.MethodGet, "/", nil)

	var problem Problem
	decode(t, rec, &problem)
	if rec.Code != http.StatusInternalServerError || problem.Code != CodeInternal {
		t.Errorf("got %d %+v", rec.Code, problem)
	}
	if strings.Contains(rec.Body.String(), "boom") || !strings.Contains(logs.String(), "boom") {
		t.Errorf("panic value must be logged, not returned: body %s, logs %s", rec.Body.String(), logs.String())
	}
}

func TestAbortHandlerPanicIsRethrown(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	handler := withRecovery(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		err, _ := recover().(error)
		if !errors.Is(err, http.ErrAbortHandler) {
			t.Errorf("recovered %v, want http.ErrAbortHandler", err)
		}
	}()
	serve(handler, http.MethodGet, "/", nil)
}
