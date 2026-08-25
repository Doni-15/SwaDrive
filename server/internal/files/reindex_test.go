package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

func TestRebuildIndexesFilesAndTrashTreesAndExcludesUploads(t *testing.T) {
	db, repository, now := openIndexTestRepository(t)
	root := t.TempDir()
	manager, err := storage.Open(root)
	if err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	defer manager.Close()

	if err := os.MkdirAll(filepath.Join(root, "files", "docs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "files", "docs", "new.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "files", "docs", "old.txt"), []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "uploads", "ignored.part"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := db.Exec(`
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES ('reindex-owner', 'test-hash', 'owner', ?, ?)
	`, now.Unix(), now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := result.LastInsertId()
	trashID := "0123456789abcdef0123456789abcdef"
	if _, err := db.Exec(`
		INSERT INTO trash_entries (id, user_id, original_path, trash_name, trashed_at, state, updated_at)
		VALUES (?, ?, 'archive', ?, ?, 'trashed', ?)
	`, trashID, userID, trashID, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "trash", trashID, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "trash", trashID, "nested", "old.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	progress, err := NewRebuilder(repository, manager, func() time.Time { return now }).Rebuild(context.Background(), nil)
	if err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
	if progress.Indexed != 6 {
		t.Fatalf("indexed entries = %d; want 6", progress.Indexed)
	}
	if _, err := repository.Metadata(context.Background(), "docs/new.txt"); err != nil {
		t.Fatalf("active nested file missing: %v", err)
	}
	if _, err := repository.Metadata(context.Background(), "ignored.part"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("upload part entered visible index: %v", err)
	}
	if _, err := repository.Metadata(context.Background(), "archive/nested/old.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("trashed subtree entered active metadata: %v", err)
	}
	var trashedRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE trash_entry_id = ?`, trashID).Scan(&trashedRows); err != nil || trashedRows != 3 {
		t.Fatalf("trashed index rows = %d, %v; want 3", trashedRows, err)
	}
}

func TestInterruptedAndCancelledRebuildLeaveOldGenerationActive(t *testing.T) {
	_, repository, now := openIndexTestRepository(t)
	createIndexedEntry(t, repository, "old.txt", KindFile, 1, now)

	failedStorage := &generatedReindexStorage{count: 250, failAfter: 130}
	progressValues := make([]int64, 0, 3)
	_, err := NewRebuilder(repository, failedStorage, func() time.Time { return now }).Rebuild(context.Background(), func(progress ReindexProgress) {
		progressValues = append(progressValues, progress.Indexed)
	})
	if err == nil {
		t.Fatal("interrupted Rebuild() succeeded")
	}
	if _, err := repository.Metadata(context.Background(), "old.txt"); err != nil {
		t.Fatalf("old generation unavailable after interrupted rebuild: %v", err)
	}
	if len(progressValues) == 0 || progressValues[0] != ReindexBatchSize {
		t.Fatalf("bounded batch progress = %v; want first flush at %d", progressValues, ReindexBatchSize)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelledStorage := &generatedReindexStorage{count: 250, cancel: cancel, cancelAfter: 20}
	_, err = NewRebuilder(repository, cancelledStorage, func() time.Time { return now }).Rebuild(ctx, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Rebuild() error = %v; want context.Canceled", err)
	}
	if _, err := repository.Metadata(context.Background(), "old.txt"); err != nil {
		t.Fatalf("old generation unavailable after cancellation: %v", err)
	}
}

type generatedReindexStorage struct {
	count       int
	failAfter   int
	cancel      context.CancelFunc
	cancelAfter int
}

func (source *generatedReindexStorage) WalkFilesForReindex(ctx context.Context, visit func(storage.ReindexEntry) error) error {
	for index := 0; index < source.count; index++ {
		if source.failAfter > 0 && index == source.failAfter {
			return errors.New("injected traversal failure")
		}
		if source.cancel != nil && index == source.cancelAfter {
			source.cancel()
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(storage.ReindexEntry{
			RelativePath: fmt.Sprintf("generated-%04d.txt", index),
			Size:         int64(index),
			ModifiedAt:   time.Unix(1_800_000_000, 0),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (*generatedReindexStorage) WalkTrashForReindex(context.Context, string, func(storage.ReindexEntry) error) error {
	return nil
}
