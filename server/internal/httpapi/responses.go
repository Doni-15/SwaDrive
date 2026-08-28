package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

const (
	maximumJSONBodyBytes int64 = 64 << 10
	jsonBodyReadTimeout        = 30 * time.Second
)

var errUnsupportedMediaType = errors.New("unsupported media type")

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, request *http.Request, status int, code, message string) {
	writeJSON(w, status, errorEnvelope{Error: apiError{
		Code:      code,
		Message:   message,
		RequestID: requestID(request),
	}})
}

func decodeJSON(w http.ResponseWriter, request *http.Request, destination any) error {
	clearDeadline, err := installReadDeadline(w, jsonBodyReadTimeout)
	if err != nil {
		return err
	}
	defer clearDeadline()
	return decodeJSONPayload(w, request, destination)
}

func decodeJSONPayload(w http.ResponseWriter, request *http.Request, destination any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errUnsupportedMediaType
	}
	request.Body = http.MaxBytesReader(w, request.Body, maximumJSONBodyBytes)
	decoder := json.NewDecoder(request.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return errors.New("JSON body must be an object")
	}
	objectDecoder := json.NewDecoder(bytes.NewReader(trimmed))
	objectDecoder.DisallowUnknownFields()
	if err := objectDecoder.Decode(destination); err != nil {
		return err
	}
	return nil
}

func installReadDeadline(w http.ResponseWriter, timeout time.Duration) (func(), error) {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		if errors.Is(err, http.ErrNotSupported) {
			return func() {}, nil
		}
		return nil, fmt.Errorf("set request read deadline: %w", err)
	}
	return func() { _ = controller.SetReadDeadline(time.Time{}) }, nil
}

func writeDecodeError(w http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, errUnsupportedMediaType) {
		writeError(w, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "The request body must use application/json.")
		return
	}
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		writeError(w, request, http.StatusRequestEntityTooLarge, "request_too_large", "The request body is too large.")
		return
	}
	writeError(w, request, http.StatusBadRequest, "invalid_json", "The JSON request body is invalid.")
}

func (server *server) writeServiceError(w http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, audit.ErrPersistence) {
		server.logger.ErrorContext(request.Context(), "audit event persistence failed", "request_id", requestID(request), "error_type", "audit_persistence")
	}
	var rateLimitError *auth.RateLimitError
	var maxBytesError *http.MaxBytesError
	switch {
	case errors.Is(err, storage.ErrUnavailable):
		writeError(w, request, http.StatusServiceUnavailable, "storage_unavailable", "Server storage is currently unavailable.")
	case errors.Is(err, files.ErrIndexInconsistent):
		server.logger.ErrorContext(request.Context(), "file metadata index is unavailable", "request_id", requestID(request), "error_type", "file_index_inconsistent")
		writeError(w, request, http.StatusServiceUnavailable, "metadata_unavailable", "File metadata is unavailable until an administrator repairs the index.")
	case errors.As(err, &rateLimitError):
		retrySeconds := int64((rateLimitError.RetryAfter + time.Second - 1) / time.Second)
		if retrySeconds < 1 {
			retrySeconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(retrySeconds, 10))
		writeError(w, request, http.StatusTooManyRequests, "rate_limited", "Too many login attempts. Try again later.")
	case errors.As(err, &maxBytesError):
		writeError(w, request, http.StatusRequestEntityTooLarge, "request_too_large", "The request body is too large.")
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, request, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password.")
	case errors.Is(err, auth.ErrUnauthorized):
		writeError(w, request, http.StatusUnauthorized, "authentication_required", "A valid bearer token is required.")
	case errors.Is(err, auth.ErrArgon2Busy), errors.Is(err, uploads.ErrUploadBusy), errors.Is(err, files.ErrDownloadBusy):
		w.Header().Set("Retry-After", "1")
		writeError(w, request, http.StatusServiceUnavailable, "server_busy", "The server is at its current resource limit. Try again shortly.")
	case errors.Is(err, files.ErrOwnerRequired), errors.Is(err, audit.ErrOwnerRoleNeeded):
		writeError(w, request, http.StatusForbidden, "forbidden", "Owner access is required.")
	case errors.Is(err, storage.ErrInvalidPath), errors.Is(err, storage.ErrSymlink):
		writeError(w, request, http.StatusBadRequest, "invalid_path", "The supplied path is invalid.")
	case errors.Is(err, storage.ErrNotFound), errors.Is(err, uploads.ErrUploadNotFound), errors.Is(err, files.ErrTrashEntryNotFound), errors.Is(err, auth.ErrSessionNotFound):
		writeError(w, request, http.StatusNotFound, "not_found", "The requested resource was not found.")
	case errors.Is(err, storage.ErrConflict), errors.Is(err, storage.ErrDirectoryNotEmpty), errors.Is(err, uploads.ErrChunkConflict), errors.Is(err, uploads.ErrUploadState), errors.Is(err, uploads.ErrMissingChunks), errors.Is(err, uploads.ErrUploadLimit), errors.Is(err, auth.ErrSessionLimit), errors.Is(err, files.ErrTrashState):
		writeError(w, request, http.StatusConflict, "conflict", "The operation conflicts with current state.")
	case errors.Is(err, uploads.ErrChecksumMismatch):
		writeError(w, request, http.StatusUnprocessableEntity, "checksum_mismatch", "The supplied checksum does not match the content.")
	case errors.Is(err, uploads.ErrChunkLength):
		writeError(w, request, http.StatusBadRequest, "invalid_chunk_length", "The chunk length is incorrect.")
	case errors.Is(err, storage.ErrInsufficientSpace):
		writeError(w, request, http.StatusInsufficientStorage, "insufficient_storage", "Insufficient free storage space.")
	case errors.Is(err, uploads.ErrInvalidUpload), errors.Is(err, files.ErrInvalidSearch), errors.Is(err, files.ErrInvalidPageCursor), errors.Is(err, audit.ErrInvalidList),
		errors.Is(err, auth.ErrInvalidUsername), errors.Is(err, auth.ErrInvalidPassword), errors.Is(err, auth.ErrInvalidClientName):
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The request is invalid.")
	case errors.Is(err, storage.ErrNotDirectory), errors.Is(err, storage.ErrNotRegularFile):
		writeError(w, request, http.StatusBadRequest, "invalid_resource_type", "The resource has the wrong type for this operation.")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, request, http.StatusRequestTimeout, "request_cancelled", "The request was cancelled.")
	default:
		server.logger.ErrorContext(request.Context(), "request operation failed", "request_id", requestID(request), "error_type", "internal")
		writeError(w, request, http.StatusInternalServerError, "internal_error", "The server could not complete the request.")
	}
}

func parseCanonicalInt64(value string, allowZero bool) (int64, error) {
	if value == "" || len(value) > 19 || len(value) > 1 && value[0] == '0' {
		return 0, errors.New("invalid decimal integer")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("invalid decimal integer")
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || !allowZero && parsed == 0 {
		return 0, errors.New("invalid decimal integer")
	}
	return parsed, nil
}

func hasOnlyQueryParameters(request *http.Request, allowed ...string) bool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for name, values := range request.URL.Query() {
		if _, ok := allowedSet[name]; !ok || len(values) != 1 {
			return false
		}
	}
	return true
}

func (server *server) notFound(w http.ResponseWriter, request *http.Request) {
	writeError(w, request, http.StatusNotFound, "not_found", "The requested endpoint was not found.")
}

func (server *server) methodNotAllowed(w http.ResponseWriter, request *http.Request) {
	writeError(w, request, http.StatusMethodNotAllowed, "method_not_allowed", "The HTTP method is not allowed for this endpoint.")
}
