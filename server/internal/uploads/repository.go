package uploads

import (
	"context"
	"errors"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/files"
)

var (
	ErrUploadNotFound             = errors.New("upload not found")
	ErrInvalidUpload              = errors.New("invalid upload")
	ErrUploadState                = errors.New("upload is not pending")
	ErrChunkLength                = errors.New("incorrect chunk length")
	ErrChunkNotFound              = errors.New("chunk not found")
	ErrChecksumMismatch           = errors.New("checksum mismatch")
	ErrChunkConflict              = errors.New("chunk conflicts with stored chunk")
	ErrMissingChunks              = errors.New("upload has missing chunks")
	ErrUploadLimit                = errors.New("active upload limit reached")
	ErrUploadBusy                 = errors.New("upload chunk queue is full")
	ErrFinalizationReconciliation = errors.New("upload finalization reconciliation requires manual review")
)

type Repository interface {
	CreateWithAudit(ctx context.Context, upload Upload, event audit.Event) error
	Find(ctx context.Context, userID int64, id string) (Upload, error)
	FindChunk(ctx context.Context, uploadID string, index int64) (Chunk, error)
	ValidateChunks(ctx context.Context, upload Upload, validateStoredChunk func(Chunk) error) error
	RecordChunk(ctx context.Context, chunk Chunk, updatedAt time.Time) error
	PrepareFinalization(ctx context.Context, userID int64, id string, from Status, wholeSHA256 []byte, updatedAt time.Time) error
	TransitionStatus(ctx context.Context, userID int64, id string, from, to Status, updatedAt time.Time) error
	CompleteWithAudit(ctx context.Context, userID int64, id string, updatedAt time.Time, entry files.Entry, event audit.Event) error
	CancelWithAudit(ctx context.Context, userID int64, id string, updatedAt time.Time, event audit.Event) error
	ListExpired(ctx context.Context, now time.Time, limit int) ([]Upload, error)
	ListFinalizing(ctx context.Context, limit int) ([]Upload, error)
	ResetFinalizing(ctx context.Context, userID int64, id string, updatedAt time.Time) error
	MarkIndexUnhealthy(ctx context.Context, reason string, updatedAt time.Time) error
	ExpireWithAudit(ctx context.Context, userID int64, id string, updatedAt time.Time, event audit.Event) error
}
