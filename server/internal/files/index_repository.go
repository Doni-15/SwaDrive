package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

type SQLiteFileIndexRepository struct {
	db    *sql.DB
	audit *audit.SQLiteRepository
}

func NewSQLiteFileIndexRepository(db *sql.DB) *SQLiteFileIndexRepository {
	return &SQLiteFileIndexRepository{db: db, audit: audit.NewSQLiteRepository(db)}
}

func (repository *SQLiteFileIndexRepository) CheckHealthy(ctx context.Context) error {
	_, err := repository.activeGeneration(ctx, repository.db)
	return err
}

func (repository *SQLiteFileIndexRepository) List(ctx context.Context, parentPath string, limit int, cursor indexCursor) ([]Entry, error) {
	generationID, err := repository.activeGeneration(ctx, repository.db)
	if err != nil {
		return nil, err
	}
	if parentPath != "" {
		parent, findErr := repository.metadataInGeneration(ctx, repository.db, generationID, parentPath)
		if findErr != nil {
			return nil, findErr
		}
		if parent.Kind != KindDirectory {
			return nil, storage.ErrNotDirectory
		}
	}
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, generation_id, logical_path, parent_path, name, normalized_name,
		       normalized_path, kind, size, modified_at, indexed_at, trash_entry_id, whole_sha256
		FROM file_entries
		WHERE generation_id = ? AND trash_entry_id IS NULL AND parent_path = ?
		  AND (? = '' OR normalized_name > ?
		       OR (normalized_name = ? AND name > ?)
		       OR (normalized_name = ? AND name = ? AND logical_path > ?))
		ORDER BY normalized_name, name, logical_path
		LIMIT ?
	`, generationID, parentPath,
		cursor.Primary, cursor.Primary,
		cursor.Primary, cursor.Secondary,
		cursor.Primary, cursor.Secondary, cursor.Tertiary,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list indexed files: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows, limit)
}

func (repository *SQLiteFileIndexRepository) Metadata(ctx context.Context, logicalPath string) (Entry, error) {
	generationID, err := repository.activeGeneration(ctx, repository.db)
	if err != nil {
		return Entry{}, err
	}
	return repository.metadataInGeneration(ctx, repository.db, generationID, logicalPath)
}

func (repository *SQLiteFileIndexRepository) Search(ctx context.Context, normalizedQuery string, limit int, cursor indexCursor) ([]Entry, error) {
	generationID, err := repository.activeGeneration(ctx, repository.db)
	if err != nil {
		return nil, err
	}
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, generation_id, logical_path, parent_path, name, normalized_name,
		       normalized_path, kind, size, modified_at, indexed_at, trash_entry_id, whole_sha256
		FROM file_entries
		WHERE generation_id = ? AND trash_entry_id IS NULL
		  AND (instr(normalized_name, ?) > 0 OR instr(normalized_path, ?) > 0)
		  AND (? = '' OR normalized_path > ?
		       OR (normalized_path = ? AND logical_path > ?))
		ORDER BY normalized_path, logical_path
		LIMIT ?
	`, generationID, normalizedQuery, normalizedQuery,
		cursor.Primary, cursor.Primary,
		cursor.Primary, cursor.Secondary,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search indexed files: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows, limit)
}

func (repository *SQLiteFileIndexRepository) CreateWithAudit(ctx context.Context, entry Entry, event audit.Event) error {
	return repository.CreateWithAuditAndRepair(ctx, entry, event, "")
}

func (repository *SQLiteFileIndexRepository) CreateWithAuditAndRepair(ctx context.Context, entry Entry, event audit.Event, repairReason string) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin indexed file creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := repository.insertActiveInTransaction(ctx, tx, entry, repairReason); err != nil {
		return err
	}
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return fmt.Errorf("append indexed file creation audit: %w", err)
	}
	if repairReason != "" {
		if err := repository.ClearHealthReasonInTransaction(ctx, tx, repairReason, entry.IndexedAt); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit indexed file creation: %w", err)
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) MoveSubtreeWithAudit(ctx context.Context, source, destination storage.Path, event audit.Event) error {
	return repository.MoveSubtreeWithAuditAndRepair(ctx, source, destination, event, "")
}

func (repository *SQLiteFileIndexRepository) MoveSubtreeWithAuditAndRepair(ctx context.Context, source, destination storage.Path, event audit.Event, repairReason string) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin indexed file move: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var generationID int64
	if repairReason == "" {
		generationID, err = repository.activeGeneration(ctx, tx)
	} else {
		generationID, err = repository.activeGenerationForRepair(ctx, tx, repairReason)
	}
	if err != nil {
		return err
	}
	if _, err := repository.metadataInGeneration(ctx, tx, generationID, source.String()); err != nil {
		return err
	}
	destinationParent := path.Dir(destination.String())
	if destinationParent == "." {
		destinationParent = ""
	}
	if destinationParent != "" {
		parentEntry, findErr := repository.metadataInGeneration(ctx, tx, generationID, destinationParent)
		if findErr != nil {
			return findErr
		}
		if parentEntry.Kind != KindDirectory {
			return storage.ErrNotDirectory
		}
	}
	if _, err := repository.metadataInGeneration(ctx, tx, generationID, destination.String()); err == nil {
		return storage.ErrConflict
	} else if !errors.Is(err, storage.ErrNotFound) {
		return err
	}

	sourceValue := source.String()
	destinationValue := destination.String()
	normalizedSource := normalizeSearchValue(sourceValue)
	normalizedDestination := normalizeSearchValue(destinationValue)
	destinationName := path.Base(destinationValue)
	result, err := tx.ExecContext(ctx, `
		UPDATE file_entries
		SET logical_path = CASE
		        WHEN logical_path = ? THEN ?
		        ELSE ? || substr(logical_path, length(?) + 1)
		    END,
		    parent_path = CASE
		        WHEN logical_path = ? THEN ?
		        WHEN parent_path = ? THEN ?
		        ELSE ? || substr(parent_path, length(?) + 1)
		    END,
		    name = CASE WHEN logical_path = ? THEN ? ELSE name END,
		    normalized_name = CASE WHEN logical_path = ? THEN ? ELSE normalized_name END,
		    normalized_path = CASE
		        WHEN logical_path = ? THEN ?
		        ELSE ? || substr(normalized_path, length(?) + 1)
		    END
		WHERE generation_id = ? AND trash_entry_id IS NULL
		  AND (logical_path = ? OR substr(logical_path, 1, length(?) + 1) = ? || '/')
	`,
		sourceValue, destinationValue, destinationValue, sourceValue,
		sourceValue, destinationParent, sourceValue, destinationValue, destinationValue, sourceValue,
		sourceValue, destinationName,
		sourceValue, normalizeSearchValue(destinationName),
		sourceValue, normalizedDestination, normalizedDestination, normalizedSource,
		generationID, sourceValue, sourceValue, sourceValue,
	)
	if err != nil {
		return fmt.Errorf("move indexed file subtree: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil || rowsAffected == 0 {
		return storage.ErrNotFound
	}
	if _, err := repository.audit.AppendInTransaction(ctx, tx, event); err != nil {
		return fmt.Errorf("append indexed file move audit: %w", err)
	}
	if repairReason != "" {
		if err := repository.ClearHealthReasonInTransaction(ctx, tx, repairReason, event.OccurredAt); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit indexed file move: %w", err)
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) InsertActiveInTransaction(ctx context.Context, tx *sql.Tx, entry Entry) error {
	return repository.insertActiveInTransaction(ctx, tx, entry, "")
}

func (repository *SQLiteFileIndexRepository) insertActiveInTransaction(ctx context.Context, tx *sql.Tx, entry Entry, repairReason string) error {
	if err := validateEntry(entry); err != nil {
		return err
	}
	var generationID int64
	var err error
	if repairReason == "" {
		generationID, err = repository.activeGeneration(ctx, tx)
	} else {
		generationID, err = repository.activeGenerationForRepair(ctx, tx, repairReason)
	}
	if err != nil {
		return err
	}
	if err := repository.requireAvailableInGeneration(ctx, tx, generationID, entry.Path); err != nil {
		return err
	}
	entry.GenerationID = generationID
	if err := insertEntry(ctx, tx, entry); err != nil {
		return err
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) BeginMutation(ctx context.Context, reason string, now time.Time) error {
	if reason == "" || len(reason) > 256 {
		return ErrIndexInconsistent
	}
	result, err := repository.db.ExecContext(ctx, `
		UPDATE file_index_state
		SET healthy = 0, unhealthy_reason = ?, updated_at = ?
		WHERE singleton = 1 AND healthy = 1
		  AND EXISTS (
		      SELECT 1 FROM file_index_generations
		      WHERE id = file_index_state.active_generation_id AND status = 'active'
		  )
	`, reason, now.UTC().Unix())
	if err != nil {
		return fmt.Errorf("begin durable file mutation: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read durable file mutation result: %w", err)
	}
	if rowsAffected != 1 {
		return ErrIndexInconsistent
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) ClearMutation(ctx context.Context, reason string, now time.Time) error {
	result, err := repository.db.ExecContext(ctx, `
		UPDATE file_index_state
		SET healthy = 1, unhealthy_reason = NULL, updated_at = ?
		WHERE singleton = 1 AND healthy = 0 AND unhealthy_reason = ?
	`, now.UTC().Unix(), reason)
	if err != nil {
		return fmt.Errorf("clear durable file mutation: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read durable file mutation clear result: %w", err)
	}
	if rowsAffected != 1 {
		return ErrIndexInconsistent
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) RequireAvailableActivePathInTransaction(ctx context.Context, tx *sql.Tx, logicalPath string) error {
	generationID, err := repository.activeGeneration(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := repository.metadataInGeneration(ctx, tx, generationID, logicalPath); err == nil {
		return storage.ErrConflict
	} else if !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	parent := parentPath(logicalPath)
	if parent == "" {
		return nil
	}
	parentEntry, err := repository.metadataInGeneration(ctx, tx, generationID, parent)
	if err != nil {
		return err
	}
	if parentEntry.Kind != KindDirectory {
		return storage.ErrNotDirectory
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) ConfirmActiveInTransaction(ctx context.Context, tx *sql.Tx, entry Entry, repairReason string) error {
	if err := validateEntry(entry); err != nil {
		return err
	}
	var generationID int64
	var err error
	if repairReason == "" {
		generationID, err = repository.activeGeneration(ctx, tx)
	} else {
		generationID, err = repository.activeGenerationForRepair(ctx, tx, repairReason)
	}
	if err != nil {
		return err
	}
	existing, err := repository.metadataInGeneration(ctx, tx, generationID, entry.Path)
	if err == nil {
		if existing.Kind != entry.Kind || existing.Size != entry.Size {
			return ErrIndexInconsistent
		}
		return nil
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	if err := repository.requireAvailableInGeneration(ctx, tx, generationID, entry.Path); err != nil {
		return err
	}
	entry.GenerationID = generationID
	return insertEntry(ctx, tx, entry)
}

func (repository *SQLiteFileIndexRepository) requireAvailableInGeneration(ctx context.Context, tx *sql.Tx, generationID int64, logicalPath string) error {
	if _, err := repository.metadataInGeneration(ctx, tx, generationID, logicalPath); err == nil {
		return storage.ErrConflict
	} else if !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	parent := parentPath(logicalPath)
	if parent == "" {
		return nil
	}
	parentEntry, err := repository.metadataInGeneration(ctx, tx, generationID, parent)
	if err != nil {
		return err
	}
	if parentEntry.Kind != KindDirectory {
		return storage.ErrNotDirectory
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) MarkUnhealthy(ctx context.Context, reason string, now time.Time) error {
	if reason == "" || len(reason) > 256 {
		return ErrIndexInconsistent
	}
	_, err := repository.db.ExecContext(ctx, `
		UPDATE file_index_state
		SET healthy = 0,
		    unhealthy_reason = CASE
		        WHEN healthy = 1 OR unhealthy_reason = ? THEN ?
		        ELSE 'multiple'
		    END,
		    updated_at = ?
		WHERE singleton = 1
	`, reason, reason, now.UTC().Unix())
	if err != nil {
		return fmt.Errorf("mark file index unhealthy: %w", err)
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) ClearHealthReasonInTransaction(ctx context.Context, tx *sql.Tx, reason string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE file_index_state
		SET healthy = 1, unhealthy_reason = NULL, updated_at = ?
		WHERE singleton = 1 AND healthy = 0 AND unhealthy_reason = ?
	`, now.UTC().Unix(), reason)
	if err != nil {
		return fmt.Errorf("clear repaired file index state: %w", err)
	}
	return nil
}

type indexRowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (repository *SQLiteFileIndexRepository) activeGeneration(ctx context.Context, querier indexRowQuerier) (int64, error) {
	var generationID int64
	var healthy int
	err := querier.QueryRowContext(ctx, `
		SELECT s.active_generation_id, s.healthy
		FROM file_index_state AS s
		JOIN file_index_generations AS g ON g.id = s.active_generation_id AND g.status = 'active'
		WHERE s.singleton = 1
	`).Scan(&generationID, &healthy)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrIndexInconsistent
	}
	if err != nil {
		return 0, fmt.Errorf("read active file index generation: %w", err)
	}
	if healthy != 1 {
		return 0, ErrIndexInconsistent
	}
	return generationID, nil
}

func (repository *SQLiteFileIndexRepository) activeGenerationForRepair(ctx context.Context, querier indexRowQuerier, reason string) (int64, error) {
	var generationID int64
	var healthy int
	var unhealthyReason sql.NullString
	err := querier.QueryRowContext(ctx, `
		SELECT s.active_generation_id, s.healthy, s.unhealthy_reason
		FROM file_index_state AS s
		JOIN file_index_generations AS g ON g.id = s.active_generation_id AND g.status = 'active'
		WHERE s.singleton = 1
	`).Scan(&generationID, &healthy, &unhealthyReason)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrIndexInconsistent
	}
	if err != nil {
		return 0, fmt.Errorf("read repairable file index generation: %w", err)
	}
	if healthy != 1 && (!unhealthyReason.Valid || unhealthyReason.String != reason) {
		return 0, ErrIndexInconsistent
	}
	return generationID, nil
}

func (repository *SQLiteFileIndexRepository) metadataInGeneration(ctx context.Context, querier indexRowQuerier, generationID int64, logicalPath string) (Entry, error) {
	entry, err := scanEntry(querier.QueryRowContext(ctx, `
		SELECT id, generation_id, logical_path, parent_path, name, normalized_name,
		       normalized_path, kind, size, modified_at, indexed_at, trash_entry_id, whole_sha256
		FROM file_entries
		WHERE generation_id = ? AND logical_path = ? AND trash_entry_id IS NULL
	`, generationID, logicalPath))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, storage.ErrNotFound
	}
	if err != nil {
		return Entry{}, fmt.Errorf("find indexed file metadata: %w", err)
	}
	return entry, nil
}

func insertEntry(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, entry Entry) error {
	_, err := executor.ExecContext(ctx, `
		INSERT INTO file_entries (
			generation_id, logical_path, parent_path, name, normalized_name,
			normalized_path, kind, size, modified_at, indexed_at, trash_entry_id, whole_sha256
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.GenerationID, entry.Path, entry.ParentPath, entry.Name, entry.NormalizedName,
		entry.NormalizedPath, entry.Kind, entry.Size, entry.ModifiedAt.UTC().Unix(), entry.IndexedAt.UTC().Unix(),
		nullableIndexString(entry.TrashEntryID), nullableIndexBytes(entry.WholeSHA256))
	if err != nil {
		return fmt.Errorf("insert file index entry: %w", err)
	}
	return nil
}

func validateEntry(entry Entry) error {
	logicalPath, err := storage.ParsePath(entry.Path, false)
	if err != nil || logicalPath.String() != entry.Path || entry.ParentPath != parentPath(entry.Path) || entry.Name != path.Base(entry.Path) ||
		entry.NormalizedName != normalizeSearchValue(entry.Name) || entry.NormalizedPath != normalizeSearchValue(entry.Path) ||
		(entry.Kind != KindFile && entry.Kind != KindDirectory) || entry.Size < 0 ||
		(len(entry.WholeSHA256) != 0 && len(entry.WholeSHA256) != 32) ||
		(entry.TrashEntryID != "" && (len(entry.TrashEntryID) < 16 || len(entry.TrashEntryID) > 128)) {
		return storage.ErrInvalidPath
	}
	return nil
}

func parentPath(value string) string {
	parent := path.Dir(value)
	if parent == "." {
		return ""
	}
	return parent
}

type entryScanner interface {
	Scan(dest ...any) error
}

func scanEntry(scanner entryScanner) (Entry, error) {
	var entry Entry
	var kind string
	var modifiedAt, indexedAt int64
	var trashEntryID sql.NullString
	var wholeSHA256 []byte
	if err := scanner.Scan(
		&entry.ID, &entry.GenerationID, &entry.Path, &entry.ParentPath, &entry.Name,
		&entry.NormalizedName, &entry.NormalizedPath, &kind, &entry.Size,
		&modifiedAt, &indexedAt, &trashEntryID, &wholeSHA256,
	); err != nil {
		return Entry{}, err
	}
	entry.Kind = Kind(kind)
	entry.ModifiedAt = time.Unix(modifiedAt, 0).UTC()
	entry.IndexedAt = time.Unix(indexedAt, 0).UTC()
	entry.TrashEntryID = trashEntryID.String
	entry.WholeSHA256 = append([]byte(nil), wholeSHA256...)
	return entry, nil
}

func scanEntries(rows *sql.Rows, capacity int) ([]Entry, error) {
	entries := make([]Entry, 0, capacity)
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan indexed file: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate indexed files: %w", err)
	}
	return entries, nil
}

func nullableIndexString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableIndexBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
