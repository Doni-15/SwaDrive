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
	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

type Dependencies struct {
	Auth    *auth.Service
	Audit   *audit.Service
	Files   *files.Service
	Uploads *uploads.Service
	Logger  *slog.Logger
}

type server struct {
	auth    *auth.Service
	audit   *audit.Service
	files   *files.Service
	uploads *uploads.Service
	logger  *slog.Logger
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
		auth:    dependencies.Auth,
		audit:   dependencies.Audit,
		files:   dependencies.Files,
		uploads: dependencies.Uploads,
		logger:  logger,
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

	handle("GET /api/v1/health", http.HandlerFunc(server.health))
	handle("POST /api/v1/auth/login", http.HandlerFunc(server.login))
	handle("POST /api/v1/auth/logout", server.requireAuthentication(http.HandlerFunc(server.logout), false))
	handle("GET /api/v1/auth/me", server.requireAuthentication(http.HandlerFunc(server.me), false))
	handle("GET /api/v1/auth/sessions", server.requireAuthentication(http.HandlerFunc(server.sessions), false))
	handle("DELETE /api/v1/auth/sessions/{id}", server.requireAuthentication(http.HandlerFunc(server.revokeSession), false))

	handle("GET /api/v1/admin/audit-events", server.requireAuthentication(http.HandlerFunc(server.auditEvents), true))

	handle("GET /api/v1/files", server.requireAuthentication(http.HandlerFunc(server.listFiles), true))
	handle("GET /api/v1/files/metadata", server.requireAuthentication(http.HandlerFunc(server.fileMetadata), true))
	handle("POST /api/v1/folders", server.requireAuthentication(http.HandlerFunc(server.createFolder), true))
	handle("POST /api/v1/files/move", server.requireAuthentication(http.HandlerFunc(server.moveFile), true))
	handle("POST /api/v1/files/trash", server.requireAuthentication(http.HandlerFunc(server.trashFile), true))
	handle("GET /api/v1/trash", server.requireAuthentication(http.HandlerFunc(server.listTrash), true))
	handle("POST /api/v1/trash/{id}/restore", server.requireAuthentication(http.HandlerFunc(server.restoreTrash), true))
	handle("GET /api/v1/files/search", server.requireAuthentication(http.HandlerFunc(server.searchFiles), true))
	handle("GET /api/v1/files/content", server.requireAuthentication(http.HandlerFunc(server.downloadFile), true))

	handle("POST /api/v1/uploads", server.requireAuthentication(http.HandlerFunc(server.createUpload), true))
	handle("GET /api/v1/uploads/{id}", server.requireAuthentication(http.HandlerFunc(server.getUpload), true))
	handle("PUT /api/v1/uploads/{id}/chunks/{index}", server.requireAuthentication(http.HandlerFunc(server.putUploadChunk), true))
	handle("POST /api/v1/uploads/{id}/complete", server.requireAuthentication(http.HandlerFunc(server.completeUpload), true))
	handle("DELETE /api/v1/uploads/{id}", server.requireAuthentication(http.HandlerFunc(server.cancelUpload), true))

	mux.HandleFunc("/api/v1/", server.notFound)
	return server.requestMiddleware(mux)
}

func (server *server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
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
