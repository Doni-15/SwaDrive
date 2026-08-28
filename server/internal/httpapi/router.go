package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

type Dependencies struct {
	Auth      *auth.Service
	Audit     *audit.Service
	Files     *files.Service
	Uploads   *uploads.Service
	Storage   StorageAvailability
	Metadata  MetadataAvailability
	Readiness ReadinessChecker
	Logger    *slog.Logger
}

type StorageAvailability interface {
	Available() bool
}

type MetadataAvailability interface {
	Available() bool
}

type ReadinessChecker interface {
	Ready(ctx context.Context) error
}

type AvailabilityFunc func() bool

func (function AvailabilityFunc) Available() bool { return function() }

type ReadinessFunc func(context.Context) error

func (function ReadinessFunc) Ready(ctx context.Context) error { return function(ctx) }

type server struct {
	auth            *auth.Service
	audit           *audit.Service
	files           *files.Service
	uploads         *uploads.Service
	storage         StorageAvailability
	metadata        MetadataAvailability
	readiness       ReadinessChecker
	logger          *slog.Logger
	loginAdmissions chan struct{}
}

type contextKey string

const (
	requestIDContextKey contextKey = "request_id"
	identityContextKey  contextKey = "identity"
)

func NewHandler(dependencies Dependencies) http.Handler {
	logger := dependencies.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	server := &server{
		auth:            dependencies.Auth,
		audit:           dependencies.Audit,
		files:           dependencies.Files,
		uploads:         dependencies.Uploads,
		storage:         dependencies.Storage,
		metadata:        dependencies.Metadata,
		readiness:       dependencies.Readiness,
		logger:          logger,
		loginAdmissions: make(chan struct{}, maximumConcurrentLoginRequests),
	}

	mux := http.NewServeMux()
	registeredPaths := make(map[string]bool)
	handle := func(pattern string, handler http.Handler) {
		mux.Handle(pattern, handler)
		_, routePath, hasMethod := strings.Cut(pattern, " ")
		if hasMethod && !registeredPaths[routePath] {
			registeredPaths[routePath] = true
			mux.HandleFunc(routePath, server.methodNotAllowed)
		}
	}
	ownerContent := func(handler http.Handler) http.Handler {
		return server.requireAuthentication(server.requireMetadata(server.requireStorage(handler)), true)
	}

	handle("GET /api/v1/health", http.HandlerFunc(server.health))
	handle("GET /api/v1/ready", http.HandlerFunc(server.ready))
	handle("POST /api/v1/auth/login", http.HandlerFunc(server.login))
	handle("POST /api/v1/auth/logout", server.requireAuthentication(http.HandlerFunc(server.logout), false))
	handle("GET /api/v1/auth/me", server.requireAuthentication(http.HandlerFunc(server.me), false))
	handle("GET /api/v1/auth/sessions", server.requireAuthentication(http.HandlerFunc(server.sessions), false))
	handle("DELETE /api/v1/auth/sessions/{id}", server.requireAuthentication(http.HandlerFunc(server.revokeSession), false))

	handle("GET /api/v1/admin/audit-events", server.requireAuthentication(http.HandlerFunc(server.auditEvents), true))

	ownerMetadata := func(handler http.Handler) http.Handler {
		return server.requireAuthentication(server.requireMetadata(handler), true)
	}
	handle("GET /api/v1/files", ownerMetadata(http.HandlerFunc(server.listFiles)))
	handle("GET /api/v1/files/metadata", ownerMetadata(http.HandlerFunc(server.fileMetadata)))
	handle("POST /api/v1/folders", ownerContent(http.HandlerFunc(server.createFolder)))
	handle("POST /api/v1/files/move", ownerContent(http.HandlerFunc(server.moveFile)))
	handle("POST /api/v1/files/trash", ownerContent(http.HandlerFunc(server.trashFile)))
	handle("GET /api/v1/trash", ownerMetadata(http.HandlerFunc(server.listTrash)))
	handle("POST /api/v1/trash/{id}/restore", ownerContent(http.HandlerFunc(server.restoreTrash)))
	handle("GET /api/v1/files/search", ownerMetadata(http.HandlerFunc(server.searchFiles)))
	handle("GET /api/v1/files/content", ownerContent(http.HandlerFunc(server.downloadFile)))

	handle("POST /api/v1/uploads", ownerContent(http.HandlerFunc(server.createUpload)))
	handle("GET /api/v1/uploads/{id}", ownerMetadata(http.HandlerFunc(server.getUpload)))
	handle("PUT /api/v1/uploads/{id}/chunks/{index}", ownerContent(http.HandlerFunc(server.putUploadChunk)))
	handle("POST /api/v1/uploads/{id}/complete", ownerContent(http.HandlerFunc(server.completeUpload)))
	handle("DELETE /api/v1/uploads/{id}", ownerContent(http.HandlerFunc(server.cancelUpload)))

	mux.HandleFunc("/api/v1/", server.notFound)
	return server.requestMiddleware(mux)
}

func (server *server) health(w http.ResponseWriter, _ *http.Request) {
	status := "ok"
	storageStatus := "available"
	if server.storage == nil || !server.storage.Available() {
		status = "degraded"
		storageStatus = "unavailable"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"` + status + `","storage":"` + storageStatus + `"}`))
}

func (server *server) ready(w http.ResponseWriter, request *http.Request) {
	if server.readiness == nil || server.readiness.Ready(request.Context()) != nil {
		writeError(w, request, http.StatusServiceUnavailable, "not_ready", "The server control plane is not ready.")
		return
	}
	storageStatus := "available"
	if server.storage == nil || !server.storage.Available() {
		storageStatus = "unavailable"
	}
	writeJSON(w, http.StatusOK, struct {
		Status  string `json:"status"`
		Storage string `json:"storage"`
	}{Status: "ready", Storage: storageStatus})
}

func (server *server) requireStorage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if server.storage == nil || !server.storage.Available() {
			server.writeServiceError(w, request, storage.ErrUnavailable)
			return
		}
		next.ServeHTTP(w, request)
	})
}

func (server *server) requireMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if server.metadata != nil && !server.metadata.Available() {
			server.writeServiceError(w, request, files.ErrIndexInconsistent)
			return
		}
		next.ServeHTTP(w, request)
	})
}

func (server *server) requireAuthentication(next http.Handler, ownerOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		authorizationValues := request.Header.Values("Authorization")
		if len(authorizationValues) != 1 || !strings.HasPrefix(authorizationValues[0], "Bearer ") {
			writeError(w, request, http.StatusUnauthorized, "authentication_required", "A valid bearer token is required.")
			return
		}
		token := strings.TrimPrefix(authorizationValues[0], "Bearer ")
		if token == "" || strings.ContainsAny(token, " \t\r\n") {
			writeError(w, request, http.StatusUnauthorized, "authentication_required", "A valid bearer token is required.")
			return
		}

		identity, err := server.auth.Authenticate(request.Context(), token, requestID(request), remoteIP(request))
		if err != nil {
			server.writeServiceError(w, request, err)
			return
		}
		if ownerOnly && identity.User.Role != auth.RoleOwner {
			writeError(w, request, http.StatusForbidden, "forbidden", "Owner access is required.")
			return
		}
		ctx := context.WithValue(request.Context(), identityContextKey, identity)
		next.ServeHTTP(w, request.WithContext(ctx))
	})
}

func identity(request *http.Request) auth.Identity {
	value, _ := request.Context().Value(identityContextKey).(auth.Identity)
	return value
}

func requestID(request *http.Request) string {
	value, _ := request.Context().Value(requestIDContextKey).(string)
	return value
}

func remoteIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	if net.ParseIP(request.RemoteAddr) != nil {
		return request.RemoteAddr
	}
	return "unknown"
}

func (server *server) requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		id := newRequestID()
		ctx := context.WithValue(request.Context(), requestIDContextKey, id)
		request = request.WithContext(ctx)

		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(request.URL.Path, "/api/v1/") && request.URL.Path != "/api/v1/health" {
			w.Header().Set("Cache-Control", "no-store")
		}

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		startedAt := time.Now()
		next.ServeHTTP(recorder, request)
		route := request.Pattern
		if route == "" {
			route = "unmatched"
		}
		server.logger.InfoContext(
			ctx,
			"http request",
			"request_id", id,
			"method", request.Method,
			"route", route,
			"status", recorder.status,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if recorder.wroteHeader {
		return
	}
	recorder.status = status
	recorder.wroteHeader = true
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(data []byte) (int, error) {
	if !recorder.wroteHeader {
		recorder.WriteHeader(http.StatusOK)
	}
	return recorder.ResponseWriter.Write(data)
}

func (recorder *statusRecorder) Unwrap() http.ResponseWriter {
	return recorder.ResponseWriter
}

func newRequestID() string {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(randomBytes)
}
