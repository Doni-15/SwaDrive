package httpapi

import (
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/files"
)

const downloadWriteIdleTimeout = 30 * time.Second

type fileEntryResponse struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	IsDir      bool   `json:"is_directory"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modified_at"`
}

type trashEntryResponse struct {
	ID           string `json:"id"`
	OriginalPath string `json:"original_path"`
	TrashedAt    int64  `json:"trashed_at"`
}

func (server *server) listFiles(w http.ResponseWriter, request *http.Request) {
	if !hasOnlyQueryParameters(request, "path", "limit", "cursor") {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The file query is invalid.")
		return
	}
	query := request.URL.Query()
	limit, ok := parseOptionalLimit(query.Get("limit"))
	if !ok {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The file limit is invalid.")
		return
	}
	page, err := server.files.List(request.Context(), identity(request), query.Get("path"), limit, query.Get("cursor"))
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Entries    []fileEntryResponse `json:"entries"`
		NextCursor string              `json:"next_cursor,omitempty"`
	}{Entries: toFileEntries(page.Entries), NextCursor: page.NextCursor})
}

func (server *server) fileMetadata(w http.ResponseWriter, request *http.Request) {
	if !hasOnlyQueryParameters(request, "path") {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The file query is invalid.")
		return
	}
	entry, err := server.files.Metadata(request.Context(), identity(request), request.URL.Query().Get("path"))
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, toFileEntry(entry))
}

func (server *server) createFolder(w http.ResponseWriter, request *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(w, request, &body); err != nil {
		writeDecodeError(w, request, err)
		return
	}
	entry, err := server.files.CreateDirectory(request.Context(), identity(request), body.Path)
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusCreated, toFileEntry(entry))
}

func (server *server) moveFile(w http.ResponseWriter, request *http.Request) {
	var body struct {
		SourcePath      string `json:"source_path"`
		DestinationPath string `json:"destination_path"`
	}
	if err := decodeJSON(w, request, &body); err != nil {
		writeDecodeError(w, request, err)
		return
	}
	if err := server.files.Move(request.Context(), identity(request), body.SourcePath, body.DestinationPath); err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (server *server) trashFile(w http.ResponseWriter, request *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(w, request, &body); err != nil {
		writeDecodeError(w, request, err)
		return
	}
	entry, err := server.files.Trash(request.Context(), identity(request), body.Path)
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, toTrashEntry(entry))
}

func (server *server) listTrash(w http.ResponseWriter, request *http.Request) {
	if !hasOnlyQueryParameters(request, "limit") {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The trash query is invalid.")
		return
	}
	limit := 0
	if value := request.URL.Query().Get("limit"); value != "" {
		parsed, err := parseCanonicalInt64(value, false)
		if err != nil {
			writeError(w, request, http.StatusBadRequest, "invalid_request", "The trash limit is invalid.")
			return
		}
		limit = int(parsed)
	}
	entries, err := server.files.ListTrash(request.Context(), identity(request), limit)
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	response := make([]trashEntryResponse, 0, len(entries))
	for _, entry := range entries {
		response = append(response, toTrashEntry(entry))
	}
	writeJSON(w, http.StatusOK, struct {
		Entries []trashEntryResponse `json:"entries"`
	}{Entries: response})
}

func (server *server) restoreTrash(w http.ResponseWriter, request *http.Request) {
	if err := server.files.Restore(request.Context(), identity(request), request.PathValue("id")); err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (server *server) searchFiles(w http.ResponseWriter, request *http.Request) {
	if !hasOnlyQueryParameters(request, "q", "limit", "cursor") {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The search query is invalid.")
		return
	}
	query := request.URL.Query()
	limit, ok := parseOptionalLimit(query.Get("limit"))
	if !ok {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The search limit is invalid.")
		return
	}
	result, err := server.files.Search(request.Context(), identity(request), query.Get("q"), limit, query.Get("cursor"))
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Entries    []fileEntryResponse `json:"entries"`
		Truncated  bool                `json:"truncated"`
		NextCursor string              `json:"next_cursor,omitempty"`
	}{Entries: toFileEntries(result.Entries), Truncated: result.NextCursor != "", NextCursor: result.NextCursor})
}

func (server *server) downloadFile(w http.ResponseWriter, request *http.Request) {
	if !hasOnlyQueryParameters(request, "path") {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The download query is invalid.")
		return
	}
	file, entry, err := server.files.OpenDownload(request.Context(), identity(request), request.URL.Query().Get("path"))
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	defer file.Close()

	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Name}))
	deadlineWriter, clearDeadline, err := installWriteProgressDeadline(w, downloadWriteIdleTimeout)
	if err != nil {
		server.logger.ErrorContext(request.Context(), "download write deadline unavailable", "request_id", requestID(request), "error_type", "write_deadline")
		w.Header().Set("Retry-After", "1")
		writeError(w, request, http.StatusServiceUnavailable, "server_busy", "The server could not start the download.")
		return
	}
	defer clearDeadline()
	http.ServeContent(deadlineWriter, request, entry.Name, entry.ModifiedAt, file)
}

type writeProgressDeadlineWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
}

func (writer *writeProgressDeadlineWriter) Write(data []byte) (int, error) {
	if err := writer.controller.SetWriteDeadline(time.Now().Add(writer.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return 0, err
	}
	return writer.ResponseWriter.Write(data)
}

func (writer *writeProgressDeadlineWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func installWriteProgressDeadline(w http.ResponseWriter, timeout time.Duration) (http.ResponseWriter, func(), error) {
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		if errors.Is(err, http.ErrNotSupported) {
			return w, func() {}, nil
		}
		return nil, nil, err
	}
	return &writeProgressDeadlineWriter{ResponseWriter: w, controller: controller, timeout: timeout}, func() {
		_ = controller.SetWriteDeadline(time.Time{})
	}, nil
}

func toFileEntries(entries []files.Entry) []fileEntryResponse {
	response := make([]fileEntryResponse, 0, len(entries))
	for _, entry := range entries {
		response = append(response, toFileEntry(entry))
	}
	return response
}

func toFileEntry(entry files.Entry) fileEntryResponse {
	return fileEntryResponse{
		Path:       entry.Path,
		Name:       entry.Name,
		IsDir:      entry.IsDirectory(),
		Size:       entry.Size,
		ModifiedAt: entry.ModifiedAt.UTC().Unix(),
	}
}

func parseOptionalLimit(value string) (int, bool) {
	if value == "" {
		return 0, true
	}
	parsed, err := parseCanonicalInt64(value, false)
	if err != nil || parsed > 1_000_000 {
		return 0, false
	}
	return int(parsed), true
}

func toTrashEntry(entry files.TrashEntry) trashEntryResponse {
	return trashEntryResponse{
		ID:           entry.ID,
		OriginalPath: entry.OriginalPath,
		TrashedAt:    entry.TrashedAt.UTC().Unix(),
	}
}
