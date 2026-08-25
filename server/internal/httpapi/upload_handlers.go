package httpapi

import (
	"encoding/hex"
	"net/http"

	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

type uploadResponse struct {
	ID             string         `json:"id"`
	TargetPath     string         `json:"target_path"`
	TotalSize      int64          `json:"total_size"`
	ChunkSize      int64          `json:"chunk_size"`
	TotalChunks    int64          `json:"total_chunks"`
	WholeSHA256    string         `json:"whole_sha256,omitempty"`
	Status         uploads.Status `json:"status"`
	CreatedAt      int64          `json:"created_at"`
	UpdatedAt      int64          `json:"updated_at"`
	ExpiresAt      int64          `json:"expires_at"`
	ReceivedChunks int64          `json:"received_chunks"`
	ReceivedBytes  int64          `json:"received_bytes"`
}

func (server *server) createUpload(w http.ResponseWriter, request *http.Request) {
	var body struct {
		TargetPath  string `json:"target_path"`
		TotalSize   int64  `json:"total_size"`
		ChunkSize   int64  `json:"chunk_size,omitempty"`
		WholeSHA256 string `json:"whole_sha256,omitempty"`
	}
	if err := decodeJSON(w, request, &body); err != nil {
		writeDecodeError(w, request, err)
		return
	}
	wholeChecksum, err := parseOptionalSHA256(body.WholeSHA256)
	if err != nil {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The whole-file checksum is invalid.")
		return
	}
	upload, err := server.uploads.Create(request.Context(), identity(request), uploads.CreateInput{
		TargetPath:  body.TargetPath,
		TotalSize:   body.TotalSize,
		ChunkSize:   body.ChunkSize,
		WholeSHA256: wholeChecksum,
	})
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUploadResponse(upload))
}

func (server *server) getUpload(w http.ResponseWriter, request *http.Request) {
	upload, err := server.uploads.Get(request.Context(), identity(request), request.PathValue("id"))
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, toUploadResponse(upload))
}

func (server *server) putUploadChunk(w http.ResponseWriter, request *http.Request) {
	index, err := parseCanonicalInt64(request.PathValue("index"), true)
	if err != nil {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The chunk index is invalid.")
		return
	}
	checksum, err := parseRequiredSHA256(request.Header.Get("X-Chunk-SHA256"))
	if err != nil {
		writeError(w, request, http.StatusBadRequest, "invalid_request", "The chunk checksum is invalid.")
		return
	}
	if request.Header.Get("Content-Type") != "application/octet-stream" {
		writeError(w, request, http.StatusUnsupportedMediaType, "unsupported_media_type", "Chunk bodies must use application/octet-stream.")
		return
	}
	expectedSize, err := server.uploads.ExpectedChunk(request.Context(), identity(request), request.PathValue("id"), index)
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	if request.ContentLength > expectedSize {
		writeError(w, request, http.StatusRequestEntityTooLarge, "request_too_large", "The chunk body is too large.")
		return
	}
	if request.ContentLength >= 0 && request.ContentLength != expectedSize {
		writeError(w, request, http.StatusBadRequest, "invalid_chunk_length", "The chunk length is incorrect.")
		return
	}
	request.Body = http.MaxBytesReader(w, request.Body, expectedSize)
	result, err := server.uploads.PutChunk(
		request.Context(),
		identity(request),
		request.PathValue("id"),
		index,
		request.Body,
		checksum,
	)
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Upload     uploadResponse `json:"upload"`
		Idempotent bool           `json:"idempotent"`
	}{Upload: toUploadResponse(result.Upload), Idempotent: result.Idempotent})
}

func (server *server) completeUpload(w http.ResponseWriter, request *http.Request) {
	upload, err := server.uploads.Complete(request.Context(), identity(request), request.PathValue("id"))
	if err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, toUploadResponse(upload))
}

func (server *server) cancelUpload(w http.ResponseWriter, request *http.Request) {
	if err := server.uploads.Cancel(request.Context(), identity(request), request.PathValue("id")); err != nil {
		server.writeServiceError(w, request, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toUploadResponse(upload uploads.Upload) uploadResponse {
	return uploadResponse{
		ID:             upload.ID,
		TargetPath:     upload.TargetPath,
		TotalSize:      upload.TotalSize,
		ChunkSize:      upload.ChunkSize,
		TotalChunks:    upload.TotalChunks,
		WholeSHA256:    hex.EncodeToString(upload.WholeSHA256),
		Status:         upload.Status,
		CreatedAt:      upload.CreatedAt.UTC().Unix(),
		UpdatedAt:      upload.UpdatedAt.UTC().Unix(),
		ExpiresAt:      upload.ExpiresAt.UTC().Unix(),
		ReceivedChunks: upload.ReceivedChunks,
		ReceivedBytes:  upload.ReceivedBytes,
	}
}

func parseOptionalSHA256(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	return parseRequiredSHA256(value)
}

func parseRequiredSHA256(value string) ([]byte, error) {
	if len(value) != 64 {
		return nil, uploads.ErrInvalidUpload
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, uploads.ErrInvalidUpload
	}
	return decoded, nil
}
