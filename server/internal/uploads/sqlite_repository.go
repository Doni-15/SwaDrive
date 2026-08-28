package uploads

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/files"
)

type SQLiteRepository struct {
	db    *sql.DB
	audit *audit.SQLiteRepository
	index *files.SQLiteFileIndexRepository
}

func NewSQLiteRepository(db *sql.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db, audit: audit.NewSQLiteRepository(db), index: files.NewSQLiteFileIndexRepository(db)}
}

func (repository *SQLiteRepository) CreateWithAudit(ctx context.Context, upload Upload, event audit.Event) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upload creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := repository.index.RequireAvailableActivePathInTransaction(ctx, tx, upload.TargetPath); err != nil {
		return err
	}
	var activeUploads int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM uploads
		WHERE user_id = ? AND status IN ('pending', 'finalizing')
	`, upload.UserID).Scan(&activeUploads); err != nil {
		return fmt.Errorf("count active uploads: %w", err)
	}
	if activeUploads >= MaximumActiveUploadsPerUser {
		return ErrUploadLimit
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO uploads (
			id, user_id, target_path, part_name, total_size, chunk_size, total_chunks,
			whole_sha256, status, created_at, updated_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		upload.ID, upload.UserID, upload.TargetPath, upload.PartName,
		upload.TotalSize, upload.ChunkSize, upload.TotalChunks,
		nullableChecksum(upload.WholeSHA256), upload.Status,
		upload.CreatedAt.UTC().Unix(), upload.UpdatedAt.UTC().Unix(), upload.ExpiresAt.UTC().Unix(),
	); err != nil {
		return fmt.Errorf("create upload: %w", err)
	}
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return fmt.Errorf("append upload creation audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upload creation: %w", err)
	}
	return nil
}

func (repository *SQLiteRepository) Find(ctx context.Context, userID int64, id string) (Upload, error) {
	row := repository.db.QueryRowContext(ctx, `
		SELECT u.id, u.user_id, u.target_path, u.part_name, u.total_size, u.chunk_size,
		       u.total_chunks, u.whole_sha256, u.status, u.created_at, u.updated_at,
		       u.expires_at, COUNT(c.chunk_index), COALESCE(SUM(c.byte_size), 0)
		FROM uploads AS u
		LEFT JOIN upload_chunks AS c ON c.upload_id = u.id
		WHERE u.id = ? AND u.user_id = ?
		GROUP BY u.id
	`, id, userID)
	upload, err := scanUpload(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrUploadNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	return upload, nil
}

func (repository *SQLiteRepository) IsKnownPart(ctx context.Context, partName string) (bool, error) {
	var exists int
	err := repository.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM uploads WHERE part_name = ?)
	`, partName).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check upload part ownership: %w", err)
	}
	return exists == 1, nil
}

func (repository *SQLiteRepository) FindChunk(ctx context.Context, uploadID string, index int64) (Chunk, error) {
	var chunk Chunk
	var receivedAt int64
	err := repository.db.QueryRowContext(ctx, `
		SELECT upload_id, chunk_index, byte_offset, byte_size, sha256, received_at
		FROM upload_chunks
		WHERE upload_id = ? AND chunk_index = ?
	`, uploadID, index).Scan(&chunk.UploadID, &chunk.Index, &chunk.Offset, &chunk.Size, &chunk.SHA256, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Chunk{}, ErrChunkNotFound
	}
	if err != nil {
		return Chunk{}, fmt.Errorf("find upload chunk: %w", err)
	}
	chunk.ReceivedAt = time.Unix(receivedAt, 0).UTC()
	return chunk, nil
}

func (repository *SQLiteRepository) ValidateChunks(ctx context.Context, upload Upload, validateStoredChunk func(Chunk) error) error {
	rows, err := repository.db.QueryContext(ctx, `
		SELECT upload_id, chunk_index, byte_offset, byte_size, sha256, received_at
		FROM upload_chunks
		WHERE upload_id = ?
		ORDER BY chunk_index
	`, upload.ID)
	if err != nil {
		return fmt.Errorf("list upload chunks: %w", err)
	}
	defer rows.Close()

	var index int64
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var chunk Chunk
		var receivedAt int64
		if err := rows.Scan(&chunk.UploadID, &chunk.Index, &chunk.Offset, &chunk.Size, &chunk.SHA256, &receivedAt); err != nil {
			return fmt.Errorf("scan upload chunk: %w", err)
		}
		offset, expectedSize, boundsErr := chunkBounds(upload, index)
		if boundsErr != nil || chunk.UploadID != upload.ID || chunk.Index != index || chunk.Offset != offset || chunk.Size != expectedSize || len(chunk.SHA256) != 32 {
			return ErrMissingChunks
		}
		if validateStoredChunk != nil {
			if err := validateStoredChunk(chunk); err != nil {
				return err
			}
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate upload chunks: %w", err)
	}
	if index != upload.TotalChunks {
		return ErrMissingChunks
	}
	return nil
}

func (repository *SQLiteRepository) RecordChunk(ctx context.Context, chunk Chunk, updatedAt time.Time) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chunk record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO upload_chunks (upload_id, chunk_index, byte_offset, byte_size, sha256, received_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (upload_id, chunk_index) DO NOTHING
	`, chunk.UploadID, chunk.Index, chunk.Offset, chunk.Size, chunk.SHA256, chunk.ReceivedAt.UTC().Unix())
	if err != nil {
		return fmt.Errorf("insert upload chunk: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read upload chunk result: %w", err)
	}
	if rowsAffected == 0 {
		return ErrChunkConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE uploads SET updated_at = ? WHERE id = ?`, updatedAt.UTC().Unix(), chunk.UploadID); err != nil {
		return fmt.Errorf("update upload progress time: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upload chunk: %w", err)
	}
	return nil
}

func (repository *SQLiteRepository) PrepareFinalization(ctx context.Context, userID int64, id string, from Status, wholeSHA256 []byte, updatedAt time.Time) error {
	result, err := repository.db.ExecContext(ctx, `
		UPDATE uploads
		SET status = 'finalizing', whole_sha256 = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND status = ?
	`, wholeSHA256, updatedAt.UTC().Unix(), id, userID, from)
	if err != nil {
		return fmt.Errorf("prepare upload finalization: %w", err)
	}
	return requireChangedUpload(result)
}

func (repository *SQLiteRepository) TransitionStatus(ctx context.Context, userID int64, id string, from, to Status, updatedAt time.Time) error {
	result, err := repository.db.ExecContext(ctx, `
		UPDATE uploads SET status = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND status = ?
	`, to, updatedAt.UTC().Unix(), id, userID, from)
	if err != nil {
		return fmt.Errorf("transition upload status: %w", err)
	}
	return requireChangedUpload(result)
}

func (repository *SQLiteRepository) CompleteWithAudit(ctx context.Context, userID int64, id string, updatedAt time.Time, entry files.Entry, event audit.Event) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upload completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	repairReason := uploadHealthReason(id)
	if err := repository.index.ConfirmActiveInTransaction(ctx, tx, entry, repairReason); err != nil {
		return fmt.Errorf("index completed upload: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE uploads SET status = 'completed', whole_sha256 = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND status = 'finalizing'
	`, entry.WholeSHA256, updatedAt.UTC().Unix(), id, userID)
	if err != nil {
		return fmt.Errorf("complete upload: %w", err)
	}
	if err := requireChangedUpload(result); err != nil {
		return err
	}
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return fmt.Errorf("append upload completion audit event: %w", err)
	}
	if err := repository.index.ClearHealthReasonInTransaction(ctx, tx, repairReason, updatedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upload completion: %w", err)
	}
	return nil
}

func (repository *SQLiteRepository) CancelWithAudit(ctx context.Context, userID int64, id string, updatedAt time.Time, event audit.Event) error {
	return repository.finishUncompleted(ctx, userID, id, StatusCancelled, updatedAt, false, event)
}

func (repository *SQLiteRepository) ListExpired(ctx context.Context, now time.Time, limit int) ([]Upload, error) {
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, user_id, target_path, part_name, total_size, chunk_size, total_chunks,
		       whole_sha256, status, created_at, updated_at, expires_at, 0, 0
		FROM uploads
		WHERE status = 'pending' AND expires_at <= ?
		ORDER BY expires_at, id
		LIMIT ?
	`, now.UTC().Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("list expired uploads: %w", err)
	}
	defer rows.Close()

	uploads := make([]Upload, 0, limit)
	for rows.Next() {
		upload, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		uploads = append(uploads, upload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired uploads: %w", err)
	}
	return uploads, nil
}

func (repository *SQLiteRepository) ListFinalizing(ctx context.Context, limit int) ([]Upload, error) {
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, user_id, target_path, part_name, total_size, chunk_size, total_chunks,
		       whole_sha256, status, created_at, updated_at, expires_at, 0, 0
		FROM uploads
		WHERE status = 'finalizing'
		ORDER BY updated_at, id
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list finalizing uploads: %w", err)
	}
	defer rows.Close()
	uploads := make([]Upload, 0, limit)
	for rows.Next() {
		upload, scanErr := scanUpload(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		uploads = append(uploads, upload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate finalizing uploads: %w", err)
	}
	return uploads, nil
}

func (repository *SQLiteRepository) ResetFinalizing(ctx context.Context, userID int64, id string, updatedAt time.Time) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finalizing upload reset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE uploads SET status = 'pending', updated_at = ?
		WHERE id = ? AND user_id = ? AND status = 'finalizing'
	`, updatedAt.UTC().Unix(), id, userID)
	if err != nil {
		return fmt.Errorf("reset finalizing upload: %w", err)
	}
	if err := requireChangedUpload(result); err != nil {
		return err
	}
	if err := repository.index.ClearHealthReasonInTransaction(ctx, tx, uploadHealthReason(id), updatedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit finalizing upload reset: %w", err)
	}
	return nil
}

func (repository *SQLiteRepository) MarkIndexUnhealthy(ctx context.Context, reason string, updatedAt time.Time) error {
	return repository.index.MarkUnhealthy(ctx, reason, updatedAt)
}

func (repository *SQLiteRepository) ExpireWithAudit(ctx context.Context, userID int64, id string, updatedAt time.Time, event audit.Event) error {
	return repository.finishUncompleted(ctx, userID, id, StatusExpired, updatedAt, true, event)
}

func (repository *SQLiteRepository) finishUncompleted(ctx context.Context, userID int64, id string, status Status, updatedAt time.Time, requireExpired bool, event audit.Event) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upload status change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE uploads
		SET status = ?, updated_at = ?
		WHERE id = ? AND user_id = ? AND status = 'pending'
		  AND (? = 0 OR expires_at <= ?)
	`, status, updatedAt.UTC().Unix(), id, userID, boolInteger(requireExpired), updatedAt.UTC().Unix())
	if err != nil {
		return fmt.Errorf("update unfinished upload: %w", err)
	}
	if err := requireChangedUpload(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM upload_chunks WHERE upload_id = ?`, id); err != nil {
		return fmt.Errorf("delete upload chunks: %w", err)
	}
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return fmt.Errorf("append upload status audit event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upload status change: %w", err)
	}
	return nil
}

type uploadScanner interface {
	Scan(dest ...any) error
}

func scanUpload(row uploadScanner) (Upload, error) {
	var upload Upload
	var wholeChecksum []byte
	var status string
	var createdAt, updatedAt, expiresAt int64
	if err := row.Scan(
		&upload.ID, &upload.UserID, &upload.TargetPath, &upload.PartName,
		&upload.TotalSize, &upload.ChunkSize, &upload.TotalChunks, &wholeChecksum,
		&status, &createdAt, &updatedAt, &expiresAt,
		&upload.ReceivedChunks, &upload.ReceivedBytes,
	); err != nil {
		return Upload{}, fmt.Errorf("scan upload: %w", err)
	}
	upload.WholeSHA256 = append([]byte(nil), wholeChecksum...)
	upload.Status = Status(status)
	upload.CreatedAt = time.Unix(createdAt, 0).UTC()
	upload.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	upload.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	return upload, nil
}

func requireChangedUpload(result sql.Result) error {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read upload update result: %w", err)
	}
	if rowsAffected == 0 {
		return ErrUploadState
	}
	return nil
}

func nullableChecksum(checksum []byte) any {
	if len(checksum) == 0 {
		return nil
	}
	return checksum
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func uploadHealthReason(id string) string {
	return "upload:" + id
}
