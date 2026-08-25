// Package uploads implements persistent resumable chunked uploads.
package uploads

import "time"

const (
	ChunkSize1MiB  int64 = 1 << 20
	ChunkSize2MiB  int64 = 2 << 20
	ChunkSize4MiB  int64 = 4 << 20
	ChunkSize8MiB  int64 = 8 << 20
	ChunkSize16MiB int64 = 16 << 20

	DefaultChunkSize                   = ChunkSize4MiB
	MaximumChunkSize                   = ChunkSize16MiB
	UploadLifetime                     = 24 * time.Hour
	DefaultReserveBytes         uint64 = 1 << 30
	DefaultConcurrentChunks            = 8
	MaximumConcurrentChunks            = 64
	MaximumChunksPerUpload             = 1_000_000
	MaximumActiveUploadsPerUser        = 100
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusFinalizing Status = "finalizing"
	StatusCompleted  Status = "completed"
	StatusCancelled  Status = "cancelled"
	StatusExpired    Status = "expired"
)

type Upload struct {
	ID             string
	UserID         int64
	TargetPath     string
	PartName       string
	TotalSize      int64
	ChunkSize      int64
	TotalChunks    int64
	WholeSHA256    []byte
	Status         Status
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      time.Time
	ReceivedChunks int64
	ReceivedBytes  int64
}

type Chunk struct {
	UploadID   string
	Index      int64
	Offset     int64
	Size       int64
	SHA256     []byte
	ReceivedAt time.Time
}

type CreateInput struct {
	TargetPath  string
	TotalSize   int64
	ChunkSize   int64
	WholeSHA256 []byte
}

type PutResult struct {
	Upload     Upload
	Idempotent bool
}
