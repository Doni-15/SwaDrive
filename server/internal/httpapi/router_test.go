package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthEndpointReportsStorageAvailability(t *testing.T) {
	tests := []struct {
		name      string
		available bool
		body      string
	}{
		{name: "available", available: true, body: `{"status":"ok","storage":"available"}`},
		{name: "unavailable", available: false, body: `{"status":"degraded","storage":"unavailable"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
			rec := httptest.NewRecorder()

			NewHandler(Dependencies{Storage: staticStorageAvailability(test.available)}).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status code = %d; want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q; want %q", got, "application/json")
			}
			if got := rec.Body.String(); got != test.body {
				t.Fatalf("body = %q; want %q", got, test.body)
			}
		})
	}
}

type staticStorageAvailability bool

func (available staticStorageAvailability) Available() bool { return bool(available) }

func TestLoginAdmissionGateRejectsExcessBeforeReadingBody(t *testing.T) {
	server := &server{
		logger:          slog.New(slog.DiscardHandler),
		loginAdmissions: make(chan struct{}, 1),
	}
	server.loginAdmissions <- struct{}{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"owner"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.login(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") != "1" {
		t.Fatalf("saturated login = %d Retry-After %q; want 503/1", recorder.Code, recorder.Header().Get("Retry-After"))
	}
}

func TestLoginInstallsAndClearsRouteSpecificReadDeadline(t *testing.T) {
	server := &server{
		logger:          slog.New(slog.DiscardHandler),
		loginAdmissions: make(chan struct{}, maximumConcurrentLoginRequests),
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{`))
	request.Header.Set("Content-Type", "application/json")
	recorder := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	server.login(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid login body status = %d; want 400", recorder.Code)
	}
	if len(recorder.deadlines) != 2 || recorder.deadlines[0].IsZero() || !recorder.deadlines[1].IsZero() {
		t.Fatalf("login read deadlines = %v; want nonzero then cleared", recorder.deadlines)
	}
	if remaining := len(server.loginAdmissions); remaining != 0 {
		t.Fatalf("login admission slots retained = %d; want 0", remaining)
	}
}

func TestDuplicateSecurityHeadersAreRejected(t *testing.T) {
	t.Run("Authorization", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		request.Header.Add("Authorization", "Bearer first")
		request.Header.Add("Authorization", "Bearer second")
		recorder := httptest.NewRecorder()
		NewHandler(Dependencies{}).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("duplicate Authorization status = %d; want 401", recorder.Code)
		}
	})

	t.Run("X-Chunk-SHA256", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPut, "/api/v1/uploads/id/chunks/0", nil)
		request.SetPathValue("index", "0")
		request.Header.Add("X-Chunk-SHA256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		request.Header.Add("X-Chunk-SHA256", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		recorder := httptest.NewRecorder()
		(&server{}).putUploadChunk(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("duplicate X-Chunk-SHA256 status = %d; want 400", recorder.Code)
		}
	})
}

type deadlineResponseRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (recorder *deadlineResponseRecorder) SetReadDeadline(deadline time.Time) error {
	recorder.deadlines = append(recorder.deadlines, deadline)
	return nil
}
