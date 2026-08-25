package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

type SQLiteTrashRepository struct {
	db    *sql.DB
	audit *audit.SQLiteRepository
	index *SQLiteFileIndexRepository
}

func NewSQLiteTrashRepository(db *sql.DB) *SQLiteTrashRepository {
	return &SQLiteTrashRepository{db: db, audit: audit.NewSQLiteRepository(db), index: NewSQLiteFileIndexRepository(db)}
}

func (repository *SQLiteTrashRepository) PrepareTrash(ctx context.Context, entry TrashEntry) error {
	result, err := repository.db.ExecContext(ctx, `
		INSERT INTO trash_entries (
			id, user_id, original_path, trash_name, trashed_at, state, updated_at
		)
		SELECT ?, ?, ?, ?, ?, 'trashing', ?
		FROM file_index_state AS s
		JOIN file_index_generations AS g
		  ON g.id = s.active_generation_id AND g.status = 'active'
		WHERE s.singleton = 1 AND s.healthy = 1
		  AND EXISTS (
			SELECT 1 FROM file_entries AS e
			WHERE e.generation_id = s.active_generation_id
			  AND e.logical_path = ? AND e.trash_entry_id IS NULL
		  )
	`, entry.ID, entry.UserID, entry.OriginalPath, entry.TrashName, entry.TrashedAt.UTC().Unix(), entry.UpdatedAt.UTC().Unix(), entry.OriginalPath)
	if err != nil {
		return fmt.Errorf("prepare trash entry: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read trash preparation result: %w", err)
	}
	if rows == 0 {
		if err := repository.index.CheckHealthy(ctx); err != nil {
			return err
		}
		return storage.ErrNotFound
	}
	return nil
}

func (repository *SQLiteTrashRepository) AbortTrash(ctx context.Context, userID int64, id string) error {
	result, err := repository.db.ExecContext(ctx, `
		DELETE FROM trash_entries
		WHERE id = ? AND user_id = ? AND state = 'trashing'
	`, id, userID)
	if err != nil {
		return fmt.Errorf("abort trash entry: %w", err)
	}
	return requireTrashChange(result)
}

func (repository *SQLiteTrashRepository) CommitTrashWithAudit(ctx context.Context, userID int64, id string, updatedAt time.Time, event audit.Event) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin trash commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var originalPath string
	if err := tx.QueryRowContext(ctx, `
		SELECT original_path FROM trash_entries
		WHERE id = ? AND user_id = ? AND state = 'trashing'
	`, id, userID).Scan(&originalPath); errors.Is(err, sql.ErrNoRows) {
		return ErrTrashState
	} else if err != nil {
		return fmt.Errorf("read trash path for commit: %w", err)
	}
	generationID, err := repository.index.activeGenerationForRepair(ctx, tx, trashHealthReason(id))
	if err != nil {
		return err
	}
	indexed, err := tx.ExecContext(ctx, `
		UPDATE file_entries SET trash_entry_id = ?
		WHERE generation_id = ? AND trash_entry_id IS NULL
		  AND (logical_path = ? OR substr(logical_path, 1, length(?) + 1) = ? || '/')
	`, id, generationID, originalPath, originalPath, originalPath)
	if err != nil {
		return fmt.Errorf("hide trashed file index subtree: %w", err)
	}
	indexedRows, err := indexed.RowsAffected()
	if err != nil || indexedRows == 0 {
		return errors.Join(ErrIndexInconsistent, err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE trash_entries SET state = 'trashed', updated_at = ?
		WHERE id = ? AND user_id = ? AND state = 'trashing'
	`, updatedAt.UTC().Unix(), id, userID)
	if err != nil {
		return fmt.Errorf("commit trash entry: %w", err)
	}
	if err := requireTrashChange(result); err != nil {
		return err
	}
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return fmt.Errorf("append trash audit event: %w", err)
	}
	if err := repository.index.ClearHealthReasonInTransaction(ctx, tx, trashHealthReason(id), updatedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit trash transaction: %w", err)
	}
	return nil
}

func (repository *SQLiteTrashRepository) List(ctx context.Context, userID int64, limit int) ([]TrashEntry, error) {
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, user_id, original_path, trash_name, trashed_at, state, updated_at
		FROM trash_entries
		WHERE user_id = ? AND state = 'trashed'
		ORDER BY trashed_at DESC, id DESC
		LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list trash entries: %w", err)
	}
	defer rows.Close()
	return scanTrashEntries(rows, limit)
}

func (repository *SQLiteTrashRepository) BeginRestore(ctx context.Context, userID int64, id string, updatedAt time.Time) (TrashEntry, error) {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return TrashEntry{}, fmt.Errorf("begin restore preparation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	generationID, err := repository.index.activeGeneration(ctx, tx)
	if err != nil {
		return TrashEntry{}, err
	}

	entry, err := scanTrashEntry(tx.QueryRowContext(ctx, `
		SELECT id, user_id, original_path, trash_name, trashed_at, state, updated_at
		FROM trash_entries
		WHERE id = ? AND user_id = ? AND state = 'trashed'
	`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return TrashEntry{}, ErrTrashEntryNotFound
	}
	if err != nil {
		return TrashEntry{}, err
	}
	var indexedRows int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM file_entries
		WHERE generation_id = ? AND trash_entry_id = ?
	`, generationID, id).Scan(&indexedRows); err != nil || indexedRows == 0 {
		return TrashEntry{}, errors.Join(ErrIndexInconsistent, err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE trash_entries SET state = 'restoring', updated_at = ?
		WHERE id = ? AND user_id = ? AND state = 'trashed'
	`, updatedAt.UTC().Unix(), id, userID)
	if err != nil {
		return TrashEntry{}, fmt.Errorf("mark trash entry restoring: %w", err)
	}
	if err := requireTrashChange(result); err != nil {
		return TrashEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return TrashEntry{}, fmt.Errorf("commit restore preparation: %w", err)
	}
	entry.State = TrashStateRestoring
	entry.UpdatedAt = updatedAt.UTC()
	return entry, nil
}

func (repository *SQLiteTrashRepository) RollbackRestore(ctx context.Context, userID int64, id string, updatedAt time.Time) error {
	result, err := repository.db.ExecContext(ctx, `
		UPDATE trash_entries SET state = 'trashed', updated_at = ?
		WHERE id = ? AND user_id = ? AND state = 'restoring'
	`, updatedAt.UTC().Unix(), id, userID)
	if err != nil {
		return fmt.Errorf("roll back restore preparation: %w", err)
	}
	return requireTrashChange(result)
}

func (repository *SQLiteTrashRepository) FinishRestoreWithAudit(ctx context.Context, userID int64, id string, event audit.Event) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin restore completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	generationID, err := repository.index.activeGenerationForRepair(ctx, tx, restoreHealthReason(id))
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE file_entries SET trash_entry_id = NULL
		WHERE generation_id = ? AND trash_entry_id = ?
	`, generationID, id)
	if err != nil {
		return fmt.Errorf("reactivate restored file index subtree: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return errors.Join(ErrIndexInconsistent, err)
	}
	result, err = tx.ExecContext(ctx, `
		DELETE FROM trash_entries
		WHERE id = ? AND user_id = ? AND state = 'restoring'
	`, id, userID)
	if err != nil {
		return fmt.Errorf("finish restored trash entry: %w", err)
	}
	if err := requireTrashChange(result); err != nil {
		return err
	}
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return fmt.Errorf("append restore audit event: %w", err)
	}
	if err := repository.index.ClearHealthReasonInTransaction(ctx, tx, restoreHealthReason(id), event.OccurredAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit restore completion: %w", err)
	}
	return nil
}

func (repository *SQLiteTrashRepository) ListReconciliation(ctx context.Context, limit int) ([]TrashEntry, error) {
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, user_id, original_path, trash_name, trashed_at, state, updated_at
		FROM trash_entries
		WHERE state != 'trashed'
		ORDER BY updated_at, id
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list trash reconciliation entries: %w", err)
	}
	defer rows.Close()
	return scanTrashEntries(rows, limit)
}

type trashScanner interface {
	Scan(dest ...any) error
}

func scanTrashEntry(row trashScanner) (TrashEntry, error) {
	var entry TrashEntry
	var trashedAt, updatedAt int64
	var state string
	if err := row.Scan(&entry.ID, &entry.UserID, &entry.OriginalPath, &entry.TrashName, &trashedAt, &state, &updatedAt); err != nil {
		return TrashEntry{}, err
	}
	entry.TrashedAt = time.Unix(trashedAt, 0).UTC()
	entry.State = TrashState(state)
	entry.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return entry, nil
}

func scanTrashEntries(rows *sql.Rows, capacity int) ([]TrashEntry, error) {
	entries := make([]TrashEntry, 0, capacity)
	for rows.Next() {
		entry, err := scanTrashEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan trash entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trash entries: %w", err)
	}
	return entries, nil
}

func requireTrashChange(result sql.Result) error {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read trash state result: %w", err)
	}
	if rowsAffected == 0 {
		return ErrTrashState
	}
	return nil
}
