package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

const (
	ReindexCleanupBatchSize = 500
	ReindexTrashBatchSize   = 100
)

type ReindexStorage interface {
	WalkFilesForReindex(ctx context.Context, visit func(storage.ReindexEntry) error) error
	WalkTrashForReindex(ctx context.Context, trashName string, visit func(storage.ReindexEntry) error) error
}

type ReindexProgress struct {
	GenerationID int64
	Indexed      int64
	ObsoleteRows int64
}

type ReindexRepository interface {
	StartGeneration(ctx context.Context, startedAt time.Time) (Generation, error)
	InsertGenerationBatch(ctx context.Context, generationID int64, entries []Entry) error
	ActivateGeneration(ctx context.Context, generationID, expectedEntries int64, completedAt time.Time) error
	CleanupObsoleteBatch(ctx context.Context, limit int) (int, error)
	ListTrashForReindex(ctx context.Context, afterID string, limit int) ([]TrashEntry, error)
}

type Rebuilder struct {
	repository ReindexRepository
	storage    ReindexStorage
	now        func() time.Time
}

func NewRebuilder(repository ReindexRepository, storageManager ReindexStorage, now func() time.Time) *Rebuilder {
	if now == nil {
		now = time.Now
	}
	return &Rebuilder{repository: repository, storage: storageManager, now: now}
}

func (rebuilder *Rebuilder) Rebuild(ctx context.Context, progress func(ReindexProgress)) (ReindexProgress, error) {
	generation, err := rebuilder.repository.StartGeneration(ctx, rebuilder.now().UTC())
	if err != nil {
		return ReindexProgress{}, err
	}
	state := ReindexProgress{GenerationID: generation.ID}
	batch := make([]Entry, 0, ReindexBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := rebuilder.repository.InsertGenerationBatch(ctx, generation.ID, batch); err != nil {
			return err
		}
		state.Indexed += int64(len(batch))
		batch = batch[:0]
		if progress != nil {
			progress(state)
		}
		return nil
	}
	appendEntry := func(logicalPath string, item storage.ReindexEntry, trashID string) error {
		parsed, err := storage.ParsePath(logicalPath, false)
		if err != nil {
			return fmt.Errorf("reindex invalid logical path: %w", err)
		}
		kind := KindFile
		if item.IsDirectory {
			kind = KindDirectory
		}
		entry, err := NewEntry(parsed, kind, item.Size, item.ModifiedAt, rebuilder.now().UTC(), trashID, nil)
		if err != nil {
			return err
		}
		batch = append(batch, entry)
		if len(batch) == cap(batch) {
			return flush()
		}
		return nil
	}

	if err := rebuilder.storage.WalkFilesForReindex(ctx, func(item storage.ReindexEntry) error {
		return appendEntry(item.RelativePath, item, "")
	}); err != nil {
		return state, fmt.Errorf("walk active files for reindex: %w", err)
	}
	if err := rebuilder.rebuildTrash(ctx, appendEntry); err != nil {
		return state, err
	}
	if err := flush(); err != nil {
		return state, err
	}
	if err := rebuilder.repository.ActivateGeneration(ctx, generation.ID, state.Indexed, rebuilder.now().UTC()); err != nil {
		return state, err
	}
	for {
		deleted, cleanupErr := rebuilder.repository.CleanupObsoleteBatch(ctx, ReindexCleanupBatchSize)
		state.ObsoleteRows += int64(deleted)
		if cleanupErr != nil {
			return state, cleanupErr
		}
		if deleted == 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return state, err
		}
	}
	if progress != nil {
		progress(state)
	}
	return state, nil
}

func (rebuilder *Rebuilder) rebuildTrash(ctx context.Context, appendEntry func(string, storage.ReindexEntry, string) error) error {
	afterID := ""
	for {
		entries, err := rebuilder.repository.ListTrashForReindex(ctx, afterID, ReindexTrashBatchSize)
		if err != nil {
			return err
		}
		for _, trashEntry := range entries {
			if trashEntry.State != TrashStateTrashed {
				return fmt.Errorf("%w: trash entry %s is not reconciled", ErrReconciliation, trashEntry.ID)
			}
			err := rebuilder.storage.WalkTrashForReindex(ctx, trashEntry.TrashName, func(item storage.ReindexEntry) error {
				logicalPath := trashEntry.OriginalPath
				if item.RelativePath != "" {
					logicalPath += "/" + item.RelativePath
				}
				return appendEntry(logicalPath, item, trashEntry.ID)
			})
			if err != nil {
				return fmt.Errorf("walk trash entry %s for reindex: %w", trashEntry.ID, err)
			}
			afterID = trashEntry.ID
		}
		if len(entries) < ReindexTrashBatchSize {
			return nil
		}
	}
}

func (repository *SQLiteFileIndexRepository) StartGeneration(ctx context.Context, startedAt time.Time) (Generation, error) {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Generation{}, fmt.Errorf("begin file index generation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE file_index_generations
		SET status = 'obsolete', completed_at = ?
		WHERE status = 'building'
	`, startedAt.UTC().Unix()); err != nil {
		return Generation{}, fmt.Errorf("obsolete interrupted index generations: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO file_index_generations (status, started_at)
		VALUES ('building', ?)
	`, startedAt.UTC().Unix())
	if err != nil {
		return Generation{}, fmt.Errorf("create file index generation: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Generation{}, fmt.Errorf("read file index generation ID: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Generation{}, fmt.Errorf("commit file index generation: %w", err)
	}
	return Generation{ID: id, Status: "building", StartedAt: startedAt.UTC()}, nil
}

func (repository *SQLiteFileIndexRepository) InsertGenerationBatch(ctx context.Context, generationID int64, entries []Entry) error {
	if len(entries) == 0 || len(entries) > ReindexBatchSize {
		return errors.New("invalid reindex batch size")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reindex batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM file_index_generations WHERE id = ?`, generationID).Scan(&status); err != nil || status != "building" {
		return errors.Join(ErrIndexInconsistent, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateEntry(entry); err != nil {
			return err
		}
		entry.GenerationID = generationID
		if err := insertEntry(ctx, tx, entry); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reindex batch: %w", err)
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) ActivateGeneration(ctx context.Context, generationID, expectedEntries int64, completedAt time.Time) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin file index activation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var actualEntries int64
	var status string
	if err := tx.QueryRowContext(ctx, `
		SELECT status, (SELECT COUNT(*) FROM file_entries WHERE generation_id = ?)
		FROM file_index_generations WHERE id = ?
	`, generationID, generationID).Scan(&status, &actualEntries); err != nil || status != "building" || actualEntries != expectedEntries {
		return errors.Join(ErrIndexInconsistent, err)
	}
	var orphanedActiveEntries int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM file_entries AS child
		WHERE child.generation_id = ? AND child.trash_entry_id IS NULL
		  AND child.parent_path != ''
		  AND NOT EXISTS (
			SELECT 1 FROM file_entries AS parent
			WHERE parent.generation_id = child.generation_id
			  AND parent.logical_path = child.parent_path
			  AND parent.kind = 'directory' AND parent.trash_entry_id IS NULL
		  )
	`, generationID).Scan(&orphanedActiveEntries); err != nil || orphanedActiveEntries != 0 {
		return errors.Join(ErrIndexInconsistent, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE file_index_generations SET status = 'obsolete'
		WHERE status = 'active'
	`); err != nil {
		return fmt.Errorf("obsolete prior file index generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE file_index_generations
		SET status = 'active', completed_at = ?
		WHERE id = ? AND status = 'building'
	`, completedAt.UTC().Unix(), generationID); err != nil {
		return fmt.Errorf("activate file index generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE file_index_state
		SET active_generation_id = ?, healthy = 1, unhealthy_reason = NULL, updated_at = ?
		WHERE singleton = 1
	`, generationID, completedAt.UTC().Unix()); err != nil {
		return fmt.Errorf("switch active file index generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit file index activation: %w", err)
	}
	return nil
}

func (repository *SQLiteFileIndexRepository) CleanupObsoleteBatch(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > ReindexCleanupBatchSize {
		return 0, errors.New("invalid obsolete cleanup batch size")
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin obsolete index cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		DELETE FROM file_entries
		WHERE id IN (
			SELECT e.id
			FROM file_entries AS e
			JOIN file_index_generations AS g ON g.id = e.generation_id
			WHERE g.status = 'obsolete'
			ORDER BY e.id
			LIMIT ?
		)
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("delete obsolete file entries: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read obsolete cleanup count: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM file_index_generations
		WHERE status = 'obsolete'
		  AND NOT EXISTS (SELECT 1 FROM file_entries WHERE generation_id = file_index_generations.id)
	`); err != nil {
		return 0, fmt.Errorf("delete empty obsolete generations: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit obsolete index cleanup: %w", err)
	}
	return int(rows), nil
}

func (repository *SQLiteFileIndexRepository) ListTrashForReindex(ctx context.Context, afterID string, limit int) ([]TrashEntry, error) {
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, user_id, original_path, trash_name, trashed_at, state, updated_at
		FROM trash_entries
		WHERE id > ?
		ORDER BY id
		LIMIT ?
	`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list trash for reindex: %w", err)
	}
	defer rows.Close()
	return scanTrashEntries(rows, limit)
}

// WriteProgress reports only bounded counters and generation IDs. It never
// exposes logical paths, physical paths, file contents, or credentials.
func WriteProgress(writer io.Writer) func(ReindexProgress) {
	return func(progress ReindexProgress) {
		_, _ = fmt.Fprintf(writer, "generation=%d indexed=%d obsolete_rows_removed=%d\n", progress.GenerationID, progress.Indexed, progress.ObsoleteRows)
	}
}
