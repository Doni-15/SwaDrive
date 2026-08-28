package httpapi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestReadinessEndpointChecksControlPlaneAndReportsStorage(t *testing.T) {
	t.Run("ready while storage degraded", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
		rec := httptest.NewRecorder()
		NewHandler(Dependencies{
			Storage:   staticStorageAvailability(false),
			Readiness: ReadinessFunc(func(context.Context) error { return nil }),
		}).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"storage":"unavailable"`) {
			t.Fatalf("ready response = %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("control plane unavailable", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
		rec := httptest.NewRecorder()
		NewHandler(Dependencies{
			Storage:   staticStorageAvailability(true),
			Readiness: ReadinessFunc(func(context.Context) error { return errors.New("database unavailable") }),
		}).ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"not_ready"`) {
			t.Fatalf("not-ready response = %d %q", rec.Code, rec.Body.String())
		}
	})
}

func TestMetadataGateFailsClosed(t *testing.T) {
	handler := (&server{metadata: staticStorageAvailability(false), logger: slog.New(slog.DiscardHandler)}).requireMetadata(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("metadata handler ran while gate was closed")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"metadata_unavailable"`) {
		t.Fatalf("metadata gate response = %d %q", rec.Code, rec.Body.String())
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
	deadlines      []time.Time
	writeDeadlines []time.Time
}

func (recorder *deadlineResponseRecorder) SetReadDeadline(deadline time.Time) error {
	recorder.deadlines = append(recorder.deadlines, deadline)
	return nil
}

func (recorder *deadlineResponseRecorder) SetWriteDeadline(deadline time.Time) error {
	recorder.writeDeadlines = append(recorder.writeDeadlines, deadline)
	return nil
}

func TestRouteSpecificTransferDeadlinesAreInstalledAndCleared(t *testing.T) {
	readRecorder := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	clearRead, err := installReadDeadline(readRecorder, chunkBodyReadTimeout)
	if err != nil {
		t.Fatal(err)
	}
	clearRead()
	if len(readRecorder.deadlines) != 2 || readRecorder.deadlines[0].IsZero() || !readRecorder.deadlines[1].IsZero() {
		t.Fatalf("read deadlines = %v; want nonzero then cleared", readRecorder.deadlines)
	}

	writeRecorder := &deadlineResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
	writer, clearWrite, err := installWriteProgressDeadline(writeRecorder, downloadWriteIdleTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	clearWrite()
	if len(writeRecorder.writeDeadlines) != 4 || writeRecorder.writeDeadlines[0].IsZero() || !writeRecorder.writeDeadlines[3].IsZero() {
		t.Fatalf("write deadlines = %v; want initial, per-write refreshes, then cleared", writeRecorder.writeDeadlines)
	}
}
