package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

const (
	testOwnerUsername = "owner.name"
	testOwnerPassword = "a correct owner passphrase"
)

type httpTestApplication struct {
	db          *sql.DB
	storageRoot string
	storage     *storage.Manager
	mutations   *storage.MutationCoordinator
	audit       *audit.Service
	auth        *auth.Service
	passwords   *auth.PasswordManager
	files       *files.Service
	uploads     *uploads.Service
	handler     http.Handler
	now         *time.Time
}

func newHTTPTestApplication(t *testing.T) *httpTestApplication {
	t.Helper()
	base := t.TempDir()
	storageRoot := filepath.Join(base, "storage")
	if err := os.Mkdir(storageRoot, 0o750); err != nil {
		t.Fatalf("create storage root: %v", err)
	}
	db, err := database.Open(context.Background(), filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	if err := database.Migrate(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("database.Migrate() error = %v", err)
	}
	storageManager, err := storage.Open(storageRoot)
	if err != nil {
		_ = db.Close()
		t.Fatalf("storage.Open() error = %v", err)
	}

	now := time.Unix(1_800_000_000, 0).UTC()
	clock := func() time.Time { return now }
	auditService := audit.NewService(audit.NewSQLiteRepository(db), clock)
	passwordManager, err := auth.NewPasswordManager(4)
	if err != nil {
		t.Fatalf("auth.NewPasswordManager() error = %v", err)
	}
	authService, err := auth.NewService(auth.NewSQLiteRepository(db), auditService, auth.NewLoginLimiter(1000), passwordManager, clock)
	if err != nil {
		_ = storageManager.Close()
		_ = db.Close()
		t.Fatalf("auth.NewService() error = %v", err)
	}
	if _, err := authService.BootstrapOwner(context.Background(), testOwnerUsername, testOwnerPassword, "test-bootstrap"); err != nil {
		_ = storageManager.Close()
		_ = db.Close()
		t.Fatalf("BootstrapOwner() error = %v", err)
	}

	application := &httpTestApplication{
		db:          db,
		storageRoot: storageRoot,
		storage:     storageManager,
		audit:       auditService,
		auth:        authService,
		passwords:   passwordManager,
		mutations:   storage.NewMutationCoordinator(),
		now:         &now,
	}
	application.files = files.NewService(storageManager, files.NewSQLiteTrashRepository(db), files.NewSQLiteFileIndexRepository(db), application.mutations, auditService, files.DefaultConcurrentDownloads, clock)
	application.rebuildHandler(0, slog.New(slog.DiscardHandler))
	t.Cleanup(func() {
		_ = storageManager.Close()
		_ = db.Close()
	})
	return application
}

func (application *httpTestApplication) reindex(t *testing.T) {
	t.Helper()
	if _, err := files.NewRebuilder(files.NewSQLiteFileIndexRepository(application.db), application.storage, nil).Rebuild(context.Background(), nil); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
}

func (application *httpTestApplication) rebuildHandler(reserve uint64, logger *slog.Logger) {
	clock := func() time.Time { return *application.now }
	application.uploads = uploads.NewService(
		uploads.NewSQLiteRepository(application.db),
		application.storage,
		application.mutations,
		application.audit,
		reserve,
		uploads.DefaultConcurrentChunks,
		clock,
	)
	application.handler = NewHandler(Dependencies{
		Auth: application.auth, Audit: application.audit, Files: application.files,
		Uploads: application.uploads, Storage: staticStorageAvailability(true), Logger: logger,
	})
}

func (application *httpTestApplication) request(method, target string, body []byte, token string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.RemoteAddr = "192.0.2.10:54321"
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	application.handler.ServeHTTP(recorder, request)
	return recorder
}

func (application *httpTestApplication) login(t *testing.T, username, password, clientName string) (string, sessionResponse, *httptest.ResponseRecorder) {
	t.Helper()
	body := mustJSON(t, map[string]any{"username": username, "password": password, "client_name": clientName})
	recorder := application.request(http.MethodPost, "/api/v1/auth/login", body, "", nil)
	if recorder.Code != http.StatusOK {
		return "", sessionResponse{}, recorder
	}
	var response struct {
		Token   string          `json:"token"`
		Session sessionResponse `json:"session"`
	}
	decodeResponse(t, recorder, &response)
	return response.Token, response.Session, recorder
}

func TestAuthenticationHTTPFlowAndStableErrors(t *testing.T) {
	application := newHTTPTestApplication(t)
	validLoginBody := mustJSON(t, map[string]any{
		"username": testOwnerUsername, "password": testOwnerPassword, "client_name": "Linux",
	})
	assertError(t, application.request(http.MethodPost, "/api/v1/auth/login", validLoginBody, "", map[string]string{
		"Content-Type": "text/plain",
	}), http.StatusUnsupportedMediaType, "unsupported_media_type")
	assertError(t, application.request(http.MethodPost, "/api/v1/auth/login", []byte("null"), "", nil), http.StatusBadRequest, "invalid_json")
	assertError(t, application.request(http.MethodPost, "/api/v1/auth/login", append(validLoginBody, []byte(` {}`)...), "", nil), http.StatusBadRequest, "invalid_json")
	assertError(t, application.request(http.MethodPost, "/api/v1/auth/login/", validLoginBody, "", nil), http.StatusNotFound, "not_found")

	wrong := application.request(http.MethodPost, "/api/v1/auth/login", mustJSON(t, map[string]any{
		"username": testOwnerUsername, "password": "wrong password value", "client_name": "Linux",
	}), "", nil)
	unknown := application.request(http.MethodPost, "/api/v1/auth/login", mustJSON(t, map[string]any{
		"username": "unknown-user", "password": "wrong password value", "client_name": "Linux",
	}), "", nil)
	assertSamePublicError(t, wrong, unknown, http.StatusUnauthorized, "invalid_credentials")

	createHTTPTestUser(t, application, "disabled-user", "disabled user password", auth.RoleMember, true)
	disabled := application.request(http.MethodPost, "/api/v1/auth/login", mustJSON(t, map[string]any{
		"username": "disabled-user", "password": "disabled user password", "client_name": "Linux",
	}), "", nil)
	unknownAgain := application.request(http.MethodPost, "/api/v1/auth/login", mustJSON(t, map[string]any{
		"username": "another-unknown", "password": "disabled user password", "client_name": "Linux",
	}), "", nil)
	assertSamePublicError(t, unknownAgain, disabled, http.StatusUnauthorized, "invalid_credentials")

	token, firstSession, recorder := application.login(t, " OWNER.NAME ", testOwnerPassword, " Linux laptop ")
	if recorder.Code != http.StatusOK || token == "" {
		t.Fatalf("correct login = %d %s; want token", recorder.Code, recorder.Body.String())
	}
	var storedTokenHash []byte
	if err := application.db.QueryRow(`SELECT token_hash FROM sessions WHERE id = ?`, firstSession.ID).Scan(&storedTokenHash); err != nil {
		t.Fatalf("query token hash: %v", err)
	}
	wantTokenHash := sha256.Sum256([]byte(token))
	if len(storedTokenHash) != sha256.Size || !bytes.Equal(storedTokenHash, wantTokenHash[:]) {
		t.Fatal("database does not contain only a fixed-size token hash")
	}

	malformed := application.request(http.MethodGet, "/api/v1/auth/me", nil, "malformed", nil)
	malformedError := assertError(t, malformed, http.StatusUnauthorized, "authentication_required")
	if malformedError.RequestID != malformed.Header().Get("X-Request-ID") {
		t.Fatalf("error request ID = %q, header = %q", malformedError.RequestID, malformed.Header().Get("X-Request-ID"))
	}

	me := application.request(http.MethodGet, "/api/v1/auth/me", nil, token, nil)
	if me.Code != http.StatusOK || strings.Contains(me.Body.String(), "password_hash") {
		t.Fatalf("GET me = %d %s", me.Code, me.Body.String())
	}
	if me.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated response Cache-Control = %q; want no-store", me.Header().Get("Cache-Control"))
	}
	assertError(t, application.request(http.MethodDelete, "/api/v1/auth/sessions/%2B1", nil, token, nil), http.StatusNotFound, "not_found")

	secondToken, secondSession, secondLogin := application.login(t, testOwnerUsername, testOwnerPassword, "Android phone")
	if secondLogin.Code != http.StatusOK {
		t.Fatalf("second login = %d %s", secondLogin.Code, secondLogin.Body.String())
	}
	sessions := application.request(http.MethodGet, "/api/v1/auth/sessions", nil, token, nil)
	if sessions.Code != http.StatusOK {
		t.Fatalf("list sessions = %d %s", sessions.Code, sessions.Body.String())
	}
	var sessionsBody struct {
		Sessions []sessionResponse `json:"sessions"`
	}
	decodeResponse(t, sessions, &sessionsBody)
	if len(sessionsBody.Sessions) != 2 {
		t.Fatalf("session count = %d; want 2", len(sessionsBody.Sessions))
	}

	revoked := application.request(http.MethodDelete, fmt.Sprintf("/api/v1/auth/sessions/%d", secondSession.ID), nil, token, nil)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke session = %d %s", revoked.Code, revoked.Body.String())
	}
	assertError(t, application.request(http.MethodGet, "/api/v1/auth/me", nil, secondToken, nil), http.StatusUnauthorized, "authentication_required")

	logout := application.request(http.MethodPost, "/api/v1/auth/logout", nil, token, nil)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", logout.Code, logout.Body.String())
	}
	assertError(t, application.request(http.MethodGet, "/api/v1/auth/me", nil, token, nil), http.StatusUnauthorized, "authentication_required")

	expiredToken, expiredSession, expiredLogin := application.login(t, testOwnerUsername, testOwnerPassword, "Expired device")
	if expiredLogin.Code != http.StatusOK {
		t.Fatalf("expired session setup = %d %s", expiredLogin.Code, expiredLogin.Body.String())
	}
	*application.now = application.now.Add(2 * time.Second)
	if _, err := application.db.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`, expiredSession.CreatedAt+1, expiredSession.ID); err != nil {
		t.Fatalf("expire HTTP session: %v", err)
	}
	assertError(t, application.request(http.MethodGet, "/api/v1/auth/me", nil, expiredToken, nil), http.StatusUnauthorized, "authentication_required")
}

func TestDegradedStorageKeepsControlPlaneReachableAndContentFailClosed(t *testing.T) {
	application := newHTTPTestApplication(t)
	storageRoot := filepath.Join(t.TempDir(), "unmounted-storage")
	if err := os.Mkdir(storageRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	provider := storage.OpenProvider(storageRoot, "expected-volume", nil)
	clock := func() time.Time { return *application.now }
	application.files = files.NewService(
		provider,
		files.NewSQLiteTrashRepository(application.db),
		files.NewSQLiteFileIndexRepository(application.db),
		application.mutations,
		application.audit,
		files.DefaultConcurrentDownloads,
		clock,
	)
	application.uploads = uploads.NewService(
		uploads.NewSQLiteRepository(application.db),
		provider,
		application.mutations,
		application.audit,
		0,
		uploads.DefaultConcurrentChunks,
		clock,
	)
	application.handler = NewHandler(Dependencies{
		Auth: application.auth, Audit: application.audit, Files: application.files,
		Uploads: application.uploads, Storage: provider, Logger: slog.New(slog.DiscardHandler),
	})

	health := application.request(http.MethodGet, "/api/v1/health", nil, "", nil)
	if health.Code != http.StatusOK || health.Body.String() != `{"status":"degraded","storage":"unavailable"}` {
		t.Fatalf("degraded health = %d %s", health.Code, health.Body.String())
	}
	assertError(t, application.request(
		http.MethodPost,
		"/api/v1/folders",
		mustJSON(t, map[string]string{"path": "unauthenticated"}),
		"",
		nil,
	), http.StatusUnauthorized, "authentication_required")
	token, _, login := application.login(t, testOwnerUsername, testOwnerPassword, "Degraded client")
	if login.Code != http.StatusOK || token == "" {
		t.Fatalf("degraded login = %d %s", login.Code, login.Body.String())
	}
	for _, target := range []string{"/api/v1/auth/me", "/api/v1/auth/sessions", "/api/v1/files"} {
		response := application.request(http.MethodGet, target, nil, token, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s while degraded = %d %s", target, response.Code, response.Body.String())
		}
	}

	folderError := assertError(t, application.request(
		http.MethodPost,
		"/api/v1/folders",
		mustJSON(t, map[string]string{"path": "must-not-exist"}),
		token,
		nil,
	), http.StatusServiceUnavailable, "storage_unavailable")
	if folderError.Code == "server_busy" {
		t.Fatal("storage unavailable was reported as server_busy")
	}
	assertError(t, application.request(
		http.MethodPost,
		"/api/v1/uploads",
		mustJSON(t, map[string]any{"target_path": "upload.bin", "total_size": 1}),
		token,
		nil,
	), http.StatusServiceUnavailable, "storage_unavailable")

	entries, err := os.ReadDir(storageRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unmounted fallback root contains %d entries; want empty", len(entries))
	}
}

func TestLoginRateLimitUsesRemotePeerAndAuditsDenial(t *testing.T) {
	application := newHTTPTestApplication(t)
	body := mustJSON(t, map[string]any{
		"username": testOwnerUsername, "password": "wrong password value", "client_name": "Linux",
	})
	for attempt := 0; attempt < auth.AccountFailureLimit; attempt++ {
		recorder := application.request(http.MethodPost, "/api/v1/auth/login", body, "", map[string]string{
			"X-Forwarded-For": fmt.Sprintf("198.51.100.%d", attempt+1),
		})
		assertError(t, recorder, http.StatusUnauthorized, "invalid_credentials")
	}
	blocked := application.request(http.MethodPost, "/api/v1/auth/login", body, "", map[string]string{"X-Forwarded-For": "203.0.113.99"})
	assertError(t, blocked, http.StatusTooManyRequests, "rate_limited")
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatal("rate-limited response lacks Retry-After")
	}
	var eventCount int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'auth.login_rate_limited'`).Scan(&eventCount); err != nil {
		t.Fatalf("count rate-limit events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("rate-limit audit count = %d; want 1", eventCount)
	}
}

func TestOwnerFileAuditAndRangeHTTP(t *testing.T) {
	application := newHTTPTestApplication(t)
	ownerToken, _, login := application.login(t, testOwnerUsername, testOwnerPassword, "Owner device")
	if login.Code != http.StatusOK {
		t.Fatalf("owner login = %d %s", login.Code, login.Body.String())
	}
	memberPassword := "a member test passphrase"
	createHTTPTestUser(t, application, "member-user", memberPassword, auth.RoleMember, false)
	memberToken, _, memberLogin := application.login(t, "member-user", memberPassword, "Member device")
	if memberLogin.Code != http.StatusOK {
		t.Fatalf("member login = %d %s", memberLogin.Code, memberLogin.Body.String())
	}
	if recorder := application.request(http.MethodGet, "/api/v1/auth/sessions", nil, memberToken, nil); recorder.Code != http.StatusOK {
		t.Fatalf("member own-session listing = %d %s", recorder.Code, recorder.Body.String())
	}
	assertError(t, application.request(http.MethodGet, "/api/v1/files", nil, memberToken, nil), http.StatusForbidden, "forbidden")
	assertError(t, application.request(http.MethodGet, "/api/v1/admin/audit-events", nil, memberToken, nil), http.StatusForbidden, "forbidden")
	assertError(t, application.request(http.MethodPost, "/api/v1/admin/reindex", nil, ownerToken, nil), http.StatusNotFound, "not_found")

	createFolder := func(path string) *httptest.ResponseRecorder {
		return application.request(http.MethodPost, "/api/v1/folders", mustJSON(t, map[string]string{"path": path}), ownerToken, nil)
	}
	if recorder := createFolder("docs"); recorder.Code != http.StatusCreated {
		t.Fatalf("create docs = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := createFolder("docs/nested"); recorder.Code != http.StatusCreated {
		t.Fatalf("create nested = %d %s", recorder.Code, recorder.Body.String())
	}
	metadata := application.request(http.MethodGet, "/api/v1/files/metadata?path=docs%2Fnested", nil, ownerToken, nil)
	if metadata.Code != http.StatusOK || !strings.Contains(metadata.Body.String(), `"path":"docs/nested"`) || !strings.Contains(metadata.Body.String(), `"is_directory":true`) {
		t.Fatalf("nested metadata = %d %s", metadata.Code, metadata.Body.String())
	}
	assertError(t, createFolder("../escape"), http.StatusBadRequest, "invalid_path")
	assertError(t, createFolder("/absolute"), http.StatusBadRequest, "invalid_path")
	assertError(t, createFolder("nul\x00name"), http.StatusBadRequest, "invalid_path")
	assertError(t, application.request(http.MethodGet, "/api/v1/files?path=%2e%2e%2fuploads", nil, ownerToken, nil), http.StatusBadRequest, "invalid_path")
	assertError(t, application.request(http.MethodPost, "/api/v1/folders", []byte(`{"path":"`+strings.Repeat("a", 70<<10)+`"}`), ownerToken, nil), http.StatusRequestEntityTooLarge, "request_too_large")
	assertError(t, application.request(http.MethodPost, "/api/v1/folders", []byte(`{"path":"unknown","unexpected":true}`), ownerToken, nil), http.StatusBadRequest, "invalid_json")

	filesRoot := filepath.Join(application.storageRoot, "files")
	if err := os.WriteFile(filepath.Join(filesRoot, "docs", "source.txt"), []byte("source"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filesRoot, "docs", "destination.txt"), []byte("destination"), 0o600); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	application.reindex(t)
	move := func(source, destination string) *httptest.ResponseRecorder {
		return application.request(http.MethodPost, "/api/v1/files/move", mustJSON(t, map[string]string{
			"source_path": source, "destination_path": destination,
		}), ownerToken, nil)
	}
	assertError(t, move("docs/source.txt", "docs/destination.txt"), http.StatusConflict, "conflict")
	if recorder := move("docs/source.txt", "docs/moved.txt"); recorder.Code != http.StatusNoContent {
		t.Fatalf("move file = %d %s", recorder.Code, recorder.Body.String())
	}

	trashRecorder := application.request(http.MethodPost, "/api/v1/files/trash", mustJSON(t, map[string]string{"path": "docs/moved.txt"}), ownerToken, nil)
	if trashRecorder.Code != http.StatusOK {
		t.Fatalf("trash file = %d %s", trashRecorder.Code, trashRecorder.Body.String())
	}
	var trashed trashEntryResponse
	decodeResponse(t, trashRecorder, &trashed)
	if trashed.ID == "" || trashed.OriginalPath != "docs/moved.txt" {
		t.Fatalf("trash response = %+v", trashed)
	}
	trashListing := application.request(http.MethodGet, "/api/v1/trash", nil, ownerToken, nil)
	if trashListing.Code != http.StatusOK || !strings.Contains(trashListing.Body.String(), trashed.ID) || strings.Contains(trashListing.Body.String(), "trash_name") {
		t.Fatalf("trash listing = %d %s", trashListing.Code, trashListing.Body.String())
	}
	if err := os.WriteFile(filepath.Join(filesRoot, "docs", "moved.txt"), []byte("conflict"), 0o600); err != nil {
		t.Fatalf("write restore conflict: %v", err)
	}
	restoreTarget := "/api/v1/trash/" + trashed.ID + "/restore"
	assertError(t, application.request(http.MethodPost, restoreTarget, nil, ownerToken, nil), http.StatusConflict, "conflict")
	if err := os.Remove(filepath.Join(filesRoot, "docs", "moved.txt")); err != nil {
		t.Fatalf("remove restore conflict: %v", err)
	}
	if recorder := application.request(http.MethodPost, restoreTarget, nil, ownerToken, nil); recorder.Code != http.StatusNoContent {
		t.Fatalf("restore file = %d %s", recorder.Code, recorder.Body.String())
	}

	content := []byte("0123456789")
	if err := os.WriteFile(filepath.Join(filesRoot, "docs", "video.bin"), content, 0o600); err != nil {
		t.Fatalf("write range file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filesRoot, "docs", "video-copy.bin"), content, 0o600); err != nil {
		t.Fatalf("write second search file: %v", err)
	}
	application.reindex(t)
	rangeResponse := application.request(
		http.MethodGet,
		"/api/v1/files/content?path="+url.QueryEscape("docs/video.bin"),
		nil,
		ownerToken,
		map[string]string{"Range": "bytes=2-5"},
	)
	if rangeResponse.Code != http.StatusPartialContent || rangeResponse.Body.String() != "2345" {
		t.Fatalf("range response = %d %q; want 206 and 2345", rangeResponse.Code, rangeResponse.Body.String())
	}
	if rangeResponse.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q", rangeResponse.Header().Get("Content-Range"))
	}
	fullResponse := application.request(
		http.MethodGet,
		"/api/v1/files/content?path="+url.QueryEscape("docs/video.bin"),
		nil,
		ownerToken,
		nil,
	)
	if fullResponse.Code != http.StatusOK || !bytes.Equal(fullResponse.Body.Bytes(), content) {
		t.Fatalf("full download = %d %q", fullResponse.Code, fullResponse.Body.Bytes())
	}
	invalidRange := application.request(
		http.MethodGet,
		"/api/v1/files/content?path="+url.QueryEscape("docs/video.bin"),
		nil,
		ownerToken,
		map[string]string{"Range": "bytes=100-200"},
	)
	if invalidRange.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("invalid range status = %d; want 416", invalidRange.Code)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(filesRoot, "escape-link")); err != nil {
		t.Fatalf("create escape symlink: %v", err)
	}
	assertError(t, application.request(http.MethodGet, "/api/v1/files/metadata?path=escape-link", nil, ownerToken, nil), http.StatusNotFound, "not_found")

	search := application.request(http.MethodGet, "/api/v1/files/search?q=video&limit=1", nil, ownerToken, nil)
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), "video") || !strings.Contains(search.Body.String(), `"truncated":true`) {
		t.Fatalf("search = %d %s", search.Code, search.Body.String())
	}
	assertError(t, application.request(http.MethodGet, "/api/v1/files/search?q=video&limit=201", nil, ownerToken, nil), http.StatusBadRequest, "invalid_request")
	listing := application.request(http.MethodGet, "/api/v1/files", nil, ownerToken, nil)
	if listing.Code != http.StatusOK || strings.Contains(listing.Body.String(), "uploads") || strings.Contains(listing.Body.String(), "trash") {
		t.Fatalf("file listing exposes internal storage: %d %s", listing.Code, listing.Body.String())
	}
	assertError(t, application.request(http.MethodGet, "/api/v1/files?path=docs&path=other", nil, ownerToken, nil), http.StatusBadRequest, "invalid_request")

	auditPage := application.request(http.MethodGet, "/api/v1/admin/audit-events?limit=200", nil, ownerToken, nil)
	if auditPage.Code != http.StatusOK || !strings.Contains(auditPage.Body.String(), audit.EventFileDownloaded) || !strings.Contains(auditPage.Body.String(), audit.EventFileRestored) {
		t.Fatalf("audit list = %d %s", auditPage.Code, auditPage.Body.String())
	}
	if !strings.Contains(auditPage.Body.String(), `"resource_path":"docs/source.txt"`) || !strings.Contains(auditPage.Body.String(), `"destination_path":"docs/moved.txt"`) {
		t.Fatalf("move audit lacks logical source/destination: %s", auditPage.Body.String())
	}
	if strings.Contains(auditPage.Body.String(), application.storageRoot) || strings.Contains(auditPage.Body.String(), ".part") {
		t.Fatalf("audit list exposes physical/internal storage names: %s", auditPage.Body.String())
	}
	for _, forbidden := range []string{ownerToken, testOwnerPassword, "password_hash", "Authorization"} {
		if strings.Contains(auditPage.Body.String(), forbidden) {
			t.Fatalf("audit list contains forbidden credential data %q", forbidden)
		}
	}
	var fileAuditTypes int
	if err := application.db.QueryRow(`
		SELECT COUNT(DISTINCT event_type)
		FROM audit_events
		WHERE event_type IN (
			'files.folder_created', 'files.moved', 'files.trashed',
			'files.restored', 'files.download_requested'
		)
	`).Scan(&fileAuditTypes); err != nil {
		t.Fatalf("count file audit types: %v", err)
	}
	if fileAuditTypes != 5 {
		t.Fatalf("file audit type count = %d; want 5", fileAuditTypes)
	}
	assertError(t, application.request(http.MethodGet, "/api/v1/admin/audit-events?limit=201", nil, ownerToken, nil), http.StatusBadRequest, "invalid_request")
	assertError(t, application.request(http.MethodDelete, "/api/v1/admin/audit-events", nil, ownerToken, nil), http.StatusMethodNotAllowed, "method_not_allowed")
}

func TestConcurrentAuthenticatedListsAndRangeDownloads(t *testing.T) {
	application := newHTTPTestApplication(t)
	token, _, login := application.login(t, testOwnerUsername, testOwnerPassword, "Concurrent reader")
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	content := bytes.Repeat([]byte("0123456789abcdef"), 64*1024)
	filePath := filepath.Join(application.storageRoot, "files", "concurrent-range.bin")
	if err := os.WriteFile(filePath, content, 0o600); err != nil {
		t.Fatalf("write concurrent range file: %v", err)
	}
	application.reindex(t)

	const clients = 16
	start := make(chan struct{})
	errorsSeen := make(chan error, clients*2)
	var wait sync.WaitGroup
	for client := range clients {
		wait.Add(2)
		go func(client int) {
			defer wait.Done()
			<-start
			response := application.request(http.MethodGet, "/api/v1/files", nil, token, nil)
			if response.Code != http.StatusOK {
				errorsSeen <- fmt.Errorf("list client %d status %d", client, response.Code)
			}
		}(client)
		go func(client int) {
			defer wait.Done()
			<-start
			response := application.request(
				http.MethodGet,
				"/api/v1/files/content?path=concurrent-range.bin",
				nil,
				token,
				map[string]string{"Range": "bytes=1024-2047"},
			)
			if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), content[1024:2048]) {
				errorsSeen <- fmt.Errorf("range client %d status %d length %d", client, response.Code, response.Body.Len())
			}
		}(client)
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
}

func TestResumableUploadHTTPIntegrityPersistenceAndCleanup(t *testing.T) {
	application := newHTTPTestApplication(t)
	token, _, login := application.login(t, testOwnerUsername, testOwnerPassword, "Upload device")
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}

	for index, chunkSize := range []int64{
		uploads.ChunkSize1MiB, uploads.ChunkSize2MiB, uploads.ChunkSize4MiB,
		uploads.ChunkSize8MiB, uploads.ChunkSize16MiB,
	} {
		upload := createUploadHTTP(t, application, token, fmt.Sprintf("allowed-%d.bin", index), 0, chunkSize, "", http.StatusCreated)
		if upload.ChunkSize != chunkSize {
			t.Fatalf("allowed chunk size = %d; want %d", upload.ChunkSize, chunkSize)
		}
		if recorder := application.request(http.MethodDelete, "/api/v1/uploads/"+upload.ID, nil, token, nil); recorder.Code != http.StatusNoContent {
			t.Fatalf("cancel allowed upload = %d %s", recorder.Code, recorder.Body.String())
		}
	}
	defaultUpload := createUploadHTTP(t, application, token, "default-size.bin", 0, 0, "", http.StatusCreated)
	if defaultUpload.ChunkSize != uploads.DefaultChunkSize {
		t.Fatalf("default chunk size = %d; want %d", defaultUpload.ChunkSize, uploads.DefaultChunkSize)
	}
	application.request(http.MethodDelete, "/api/v1/uploads/"+defaultUpload.ID, nil, token, nil)
	createUploadHTTP(t, application, token, "invalid-size.bin", 0, 3<<20, "", http.StatusBadRequest)

	application.rebuildHandler(math.MaxUint64, slog.New(slog.DiscardHandler))
	createUploadHTTP(t, application, token, "no-space.bin", 0, uploads.ChunkSize1MiB, "", http.StatusInsufficientStorage)
	application.rebuildHandler(0, slog.New(slog.DiscardHandler))

	firstChunk := bytes.Repeat([]byte{0x41}, int(uploads.ChunkSize1MiB))
	lastChunk := []byte("end")
	completeBytes := append(append([]byte(nil), firstChunk...), lastChunk...)
	wholeChecksum := sha256.Sum256(completeBytes)
	upload := createUploadHTTP(
		t, application, token, "large.bin", int64(len(completeBytes)), uploads.ChunkSize1MiB,
		hex.EncodeToString(wholeChecksum[:]), http.StatusCreated,
	)
	listingBefore := application.request(http.MethodGet, "/api/v1/files", nil, token, nil)
	if strings.Contains(listingBefore.Body.String(), "large.bin") {
		t.Fatal("partial upload appeared in normal files listing")
	}

	chunkTarget := fmt.Sprintf("/api/v1/uploads/%s/chunks/0", upload.ID)
	oversized := append(append([]byte(nil), firstChunk...), 0)
	oversizedChecksum := sha256.Sum256(oversized)
	assertError(t, application.request(http.MethodPut, chunkTarget, oversized, token, map[string]string{
		"Content-Type": "application/octet-stream", "X-Chunk-SHA256": hex.EncodeToString(oversizedChecksum[:]),
	}), http.StatusRequestEntityTooLarge, "request_too_large")
	shortChecksum := sha256.Sum256([]byte("short"))
	assertError(t, application.request(http.MethodPut, chunkTarget, []byte("short"), token, map[string]string{
		"Content-Type": "application/octet-stream", "X-Chunk-SHA256": hex.EncodeToString(shortChecksum[:]),
	}), http.StatusBadRequest, "invalid_chunk_length")
	assertError(t, application.request(http.MethodPut, chunkTarget, firstChunk, token, map[string]string{
		"Content-Type": "application/octet-stream", "X-Chunk-SHA256": strings.Repeat("0", 64),
	}), http.StatusUnprocessableEntity, "checksum_mismatch")
	var recordedAfterBadChecksum int
	if err := application.db.QueryRow(`SELECT COUNT(*) FROM upload_chunks WHERE upload_id = ?`, upload.ID).Scan(&recordedAfterBadChecksum); err != nil {
		t.Fatalf("count chunks after bad checksum: %v", err)
	}
	if recordedAfterBadChecksum != 0 {
		t.Fatalf("chunks recorded after bad checksum = %d; want 0", recordedAfterBadChecksum)
	}

	firstChecksum := sha256.Sum256(firstChunk)
	putChunk := application.request(http.MethodPut, chunkTarget, firstChunk, token, map[string]string{
		"Content-Type": "application/octet-stream", "X-Chunk-SHA256": hex.EncodeToString(firstChecksum[:]),
	})
	if putChunk.Code != http.StatusOK {
		t.Fatalf("put chunk = %d %s", putChunk.Code, putChunk.Body.String())
	}
	idempotent := application.request(http.MethodPut, chunkTarget, firstChunk, token, map[string]string{
		"Content-Type": "application/octet-stream", "X-Chunk-SHA256": hex.EncodeToString(firstChecksum[:]),
	})
	if idempotent.Code != http.StatusOK || !strings.Contains(idempotent.Body.String(), `"idempotent":true`) {
		t.Fatalf("idempotent retry = %d %s", idempotent.Code, idempotent.Body.String())
	}
	conflictingChunk := bytes.Repeat([]byte{0x42}, int(uploads.ChunkSize1MiB))
	conflictingChecksum := sha256.Sum256(conflictingChunk)
	assertError(t, application.request(http.MethodPut, chunkTarget, conflictingChunk, token, map[string]string{
		"Content-Type": "application/octet-stream", "X-Chunk-SHA256": hex.EncodeToString(conflictingChecksum[:]),
	}), http.StatusConflict, "conflict")
	assertError(t, application.request(http.MethodPost, "/api/v1/uploads/"+upload.ID+"/complete", nil, token, nil), http.StatusConflict, "conflict")

	lastChecksum := sha256.Sum256(lastChunk)
	lastTarget := fmt.Sprintf("/api/v1/uploads/%s/chunks/1", upload.ID)
	if recorder := application.request(http.MethodPut, lastTarget, lastChunk, token, map[string]string{
		"Content-Type": "application/octet-stream", "X-Chunk-SHA256": hex.EncodeToString(lastChecksum[:]),
	}); recorder.Code != http.StatusOK {
		t.Fatalf("put final chunk = %d %s", recorder.Code, recorder.Body.String())
	}

	application.rebuildHandler(0, slog.New(slog.DiscardHandler))
	resumed := application.request(http.MethodGet, "/api/v1/uploads/"+upload.ID, nil, token, nil)
	if resumed.Code != http.StatusOK || !strings.Contains(resumed.Body.String(), `"received_chunks":2`) {
		t.Fatalf("resumed upload = %d %s", resumed.Code, resumed.Body.String())
	}
	completed := application.request(http.MethodPost, "/api/v1/uploads/"+upload.ID+"/complete", nil, token, nil)
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"status":"completed"`) {
		t.Fatalf("complete upload = %d %s", completed.Code, completed.Body.String())
	}
	finalBytes, err := os.ReadFile(filepath.Join(application.storageRoot, "files", "large.bin"))
	if err != nil || !bytes.Equal(finalBytes, completeBytes) {
		t.Fatalf("final file length = %d, error = %v; bytes do not match", len(finalBytes), err)
	}

	conflictUpload := createUploadHTTP(t, application, token, "completion-conflict.bin", int64(len(lastChunk)), uploads.ChunkSize1MiB, "", http.StatusCreated)
	conflictTarget := fmt.Sprintf("/api/v1/uploads/%s/chunks/0", conflictUpload.ID)
	if recorder := application.request(http.MethodPut, conflictTarget, lastChunk, token, map[string]string{
		"Content-Type": "application/octet-stream", "X-Chunk-SHA256": hex.EncodeToString(lastChecksum[:]),
	}); recorder.Code != http.StatusOK {
		t.Fatalf("put conflict upload chunk = %d %s", recorder.Code, recorder.Body.String())
	}
	if err := os.WriteFile(filepath.Join(application.storageRoot, "files", "completion-conflict.bin"), []byte("occupied"), 0o600); err != nil {
		t.Fatalf("write completion conflict: %v", err)
	}
	assertError(t, application.request(http.MethodPost, "/api/v1/uploads/"+conflictUpload.ID+"/complete", nil, token, nil), http.StatusConflict, "conflict")

	badWholeUpload := createUploadHTTP(t, application, token, "bad-whole.bin", int64(len(lastChunk)), uploads.ChunkSize1MiB, strings.Repeat("0", 64), http.StatusCreated)
	badWholeTarget := fmt.Sprintf("/api/v1/uploads/%s/chunks/0", badWholeUpload.ID)
	if recorder := application.request(http.MethodPut, badWholeTarget, lastChunk, token, map[string]string{
		"Content-Type": "application/octet-stream", "X-Chunk-SHA256": hex.EncodeToString(lastChecksum[:]),
	}); recorder.Code != http.StatusOK {
		t.Fatalf("put bad-whole chunk = %d %s", recorder.Code, recorder.Body.String())
	}
	assertError(t, application.request(http.MethodPost, "/api/v1/uploads/"+badWholeUpload.ID+"/complete", nil, token, nil), http.StatusUnprocessableEntity, "checksum_mismatch")

	cancelledUpload := createUploadHTTP(t, application, token, "cancelled.bin", int64(len(lastChunk)), uploads.ChunkSize1MiB, "", http.StatusCreated)
	var cancelledPartName string
	if err := application.db.QueryRow(`SELECT part_name FROM uploads WHERE id = ?`, cancelledUpload.ID).Scan(&cancelledPartName); err != nil {
		t.Fatalf("query cancelled part: %v", err)
	}
	if recorder := application.request(http.MethodDelete, "/api/v1/uploads/"+cancelledUpload.ID, nil, token, nil); recorder.Code != http.StatusNoContent {
		t.Fatalf("cancel upload = %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(application.storageRoot, "uploads", cancelledPartName)); !os.IsNotExist(err) {
		t.Fatalf("cancelled part still exists: %v", err)
	}

	expiredUpload := createUploadHTTP(t, application, token, "expired.bin", int64(len(lastChunk)), uploads.ChunkSize1MiB, "", http.StatusCreated)
	var expiredPartName string
	if err := application.db.QueryRow(`SELECT part_name FROM uploads WHERE id = ?`, expiredUpload.ID).Scan(&expiredPartName); err != nil {
		t.Fatalf("query expired part: %v", err)
	}
	*application.now = application.now.Add(uploads.UploadLifetime + time.Second)
	cleaned, err := application.uploads.CleanupExpired(context.Background())
	if err != nil || cleaned < 1 {
		t.Fatalf("CleanupExpired() = %d, %v; want at least 1", cleaned, err)
	}
	if _, err := os.Stat(filepath.Join(application.storageRoot, "uploads", expiredPartName)); !os.IsNotExist(err) {
		t.Fatalf("expired part still exists: %v", err)
	}
	var expiredStatus string
	if err := application.db.QueryRow(`SELECT status FROM uploads WHERE id = ?`, expiredUpload.ID).Scan(&expiredStatus); err != nil {
		t.Fatalf("query expired status: %v", err)
	}
	if expiredStatus != string(uploads.StatusExpired) {
		t.Fatalf("expired status = %q; want %q", expiredStatus, uploads.StatusExpired)
	}

	var requiredAuditTypes int
	if err := application.db.QueryRow(`
		SELECT COUNT(DISTINCT event_type)
		FROM audit_events
		WHERE event_type IN ('uploads.created', 'uploads.completed', 'uploads.cancelled', 'uploads.security_failure')
	`).Scan(&requiredAuditTypes); err != nil {
		t.Fatalf("count upload audit types: %v", err)
	}
	if requiredAuditTypes != 4 {
		t.Fatalf("upload audit type count = %d; want 4", requiredAuditTypes)
	}
}

func TestConcurrentUploadChunkRetriesAreSafe(t *testing.T) {
	application := newHTTPTestApplication(t)
	token, _, login := application.login(t, testOwnerUsername, testOwnerPassword, "Concurrent upload device")
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	content := []byte("concurrent chunk")
	upload := createUploadHTTP(t, application, token, "concurrent.bin", int64(len(content)), uploads.ChunkSize1MiB, "", http.StatusCreated)
	checksum := sha256.Sum256(content)
	target := fmt.Sprintf("/api/v1/uploads/%s/chunks/0", upload.ID)
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			responses <- application.request(http.MethodPut, target, content, token, map[string]string{
				"Content-Type": "application/octet-stream", "X-Chunk-SHA256": hex.EncodeToString(checksum[:]),
			})
		}()
	}
	for range 2 {
		response := <-responses
		if response.Code != http.StatusOK {
			t.Fatalf("concurrent chunk response = %d %s", response.Code, response.Body.String())
		}
	}
	if recorder := application.request(http.MethodPost, "/api/v1/uploads/"+upload.ID+"/complete", nil, token, nil); recorder.Code != http.StatusOK {
		t.Fatalf("complete concurrent upload = %d %s", recorder.Code, recorder.Body.String())
	}
	finalBytes, err := os.ReadFile(filepath.Join(application.storageRoot, "files", "concurrent.bin"))
	if err != nil || !bytes.Equal(finalBytes, content) {
		t.Fatalf("concurrent final bytes = %q, %v", finalBytes, err)
	}
}

func TestOperationalLogsRedactCredentialsAndQueries(t *testing.T) {
	application := newHTTPTestApplication(t)
	token, _, login := application.login(t, testOwnerUsername, testOwnerPassword, "Logging device")
	if login.Code != http.StatusOK {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	var logs bytes.Buffer
	application.rebuildHandler(0, slog.New(slog.NewJSONHandler(&logs, nil)))
	clientRequestID := "client-request-id-must-not-be-trusted"
	recorder := application.request(http.MethodGet, "/api/v1/files?path=private-name", nil, token, map[string]string{"X-Request-ID": clientRequestID})
	if recorder.Header().Get("X-Request-ID") == clientRequestID {
		t.Fatal("server trusted client-provided request ID")
	}
	logOutput := logs.String()
	if strings.Contains(logOutput, token) || strings.Contains(logOutput, "Authorization") || strings.Contains(logOutput, "private-name") || strings.Contains(logOutput, testOwnerPassword) {
		t.Fatalf("operational log contains credential, header, query, or password: %s", logOutput)
	}
	if !strings.Contains(logOutput, `"request_id"`) || !strings.Contains(logOutput, `"route"`) || !strings.Contains(logOutput, `"duration_ms"`) {
		t.Fatalf("structured log lacks operational fields: %s", logOutput)
	}
}

func createHTTPTestUser(t *testing.T, application *httpTestApplication, username, password string, role auth.Role, disabled bool) int64 {
	t.Helper()
	passwordHash, err := application.passwords.Hash(context.Background(), password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	var disabledAt any
	if disabled {
		disabledAt = application.now.Unix()
	}
	result, err := application.db.Exec(`
		INSERT INTO users (username, password_hash, role, created_at, updated_at, disabled_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, username, passwordHash, role, application.now.Unix(), application.now.Unix(), disabledAt)
	if err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read test user ID: %v", err)
	}
	return id
}

func createUploadHTTP(
	t *testing.T,
	application *httpTestApplication,
	token, targetPath string,
	totalSize, chunkSize int64,
	wholeChecksum string,
	wantStatus int,
) uploadResponse {
	t.Helper()
	recorder := application.request(http.MethodPost, "/api/v1/uploads", mustJSON(t, map[string]any{
		"target_path": targetPath, "total_size": totalSize, "chunk_size": chunkSize, "whole_sha256": wholeChecksum,
	}), token, nil)
	if recorder.Code != wantStatus {
		t.Fatalf("create upload %q = %d %s; want %d", targetPath, recorder.Code, recorder.Body.String(), wantStatus)
	}
	if wantStatus != http.StatusCreated {
		return uploadResponse{}
	}
	var upload uploadResponse
	decodeResponse(t, recorder, &upload)
	return upload
}

func assertSamePublicError(t *testing.T, first, second *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	firstError := assertError(t, first, status, code)
	secondError := assertError(t, second, status, code)
	firstError.RequestID = ""
	secondError.RequestID = ""
	if firstError != secondError {
		t.Fatalf("public errors differ: %+v vs %+v", firstError, secondError)
	}
}

func assertError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) apiError {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, body = %s; want %d", recorder.Code, recorder.Body.String(), status)
	}
	var response errorEnvelope
	decodeResponse(t, recorder, &response)
	if response.Error.Code != code || response.Error.Message == "" || response.Error.RequestID == "" {
		t.Fatalf("error = %+v; want code %q with safe message and request ID", response.Error, code)
	}
	return response.Error
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return encoded
}

func decodeResponse(t *testing.T, recorder *httptest.ResponseRecorder, destination any) {
	t.Helper()
	if err := json.NewDecoder(recorder.Body).Decode(destination); err != nil && err != io.EOF {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
}
