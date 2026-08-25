package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

func TestFileIndexCRUDPaginationSearchAndActiveUniqueness(t *testing.T) {
	db, repository, now := openIndexTestRepository(t)
	ctx := context.Background()

	createIndexedEntry(t, repository, "docs", KindDirectory, 0, now)
	createIndexedEntry(t, repository, "docs/Beta.txt", KindFile, 4, now)
	createIndexedEntry(t, repository, "docs/alpha.txt", KindFile, 5, now)
	createIndexedEntry(t, repository, "docs/ALPHA.txt", KindFile, 6, now)

	if _, err := repository.Metadata(ctx, "docs/alpha.txt"); err != nil {
		t.Fatalf("Metadata() error = %v", err)
	}
	first, err := repository.List(ctx, "docs", 2, indexCursor{})
	if err != nil {
		t.Fatalf("List(first) error = %v", err)
	}
	if len(first) != 2 || first[0].Name != "ALPHA.txt" || first[1].Name != "alpha.txt" {
		t.Fatalf("first ordered page = %+v", first)
	}
	second, err := repository.List(ctx, "docs", 2, indexCursor{
		Primary: first[1].NormalizedName, Secondary: first[1].Name, Tertiary: first[1].Path,
	})
	if err != nil || len(second) != 1 || second[0].Name != "Beta.txt" {
		t.Fatalf("second ordered page = %+v, %v", second, err)
	}

	search, err := repository.Search(ctx, "docs/alpha", 10, indexCursor{})
	if err != nil || len(search) != 2 {
		t.Fatalf("Search(path) count = %d, error = %v", len(search), err)
	}
	search, err = repository.Search(ctx, "beta", 10, indexCursor{})
	if err != nil || len(search) != 1 || search[0].Path != "docs/Beta.txt" {
		t.Fatalf("Search(name) = %+v, %v", search, err)
	}

	duplicatePath, _ := storage.ParsePath("docs/alpha.txt", false)
	duplicate, _ := NewEntry(duplicatePath, KindFile, 7, now, now, "", nil)
	if err := repository.CreateWithAudit(ctx, duplicate, testAuditEvent("test.duplicate")); err == nil {
		t.Fatal("duplicate active path insert succeeded")
	}

	trashID := "0123456789abcdef0123456789abcdef"
	if _, err := db.ExecContext(ctx, `
		UPDATE file_entries SET trash_entry_id = ?
		WHERE generation_id = 1 AND logical_path = 'docs/alpha.txt'
	`, trashID); err != nil {
		t.Fatalf("mark old entry trashed: %v", err)
	}
	if err := repository.CreateWithAudit(ctx, duplicate, testAuditEvent("test.replacement")); err != nil {
		t.Fatalf("active replacement alongside trashed path error = %v", err)
	}
	var samePathRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_entries WHERE logical_path = 'docs/alpha.txt'`).Scan(&samePathRows); err != nil || samePathRows != 2 {
		t.Fatalf("same-path rows = %d, %v; want 2", samePathRows, err)
	}
}

func TestGenerationSwitchInterruptedBuildAndObsoleteCleanup(t *testing.T) {
	db, repository, now := openIndexTestRepository(t)
	ctx := context.Background()
	createIndexedEntry(t, repository, "old.txt", KindFile, 1, now)

	building, err := repository.StartGeneration(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("StartGeneration() error = %v", err)
	}
	newPath, _ := storage.ParsePath("half-built.txt", false)
	halfBuilt, _ := NewEntry(newPath, KindFile, 2, now, now, "", nil)
	if err := repository.InsertGenerationBatch(ctx, building.ID, []Entry{halfBuilt}); err != nil {
		t.Fatalf("InsertGenerationBatch() error = %v", err)
	}
	if _, err := repository.Metadata(ctx, "old.txt"); err != nil {
		t.Fatalf("old generation disappeared during interrupted build: %v", err)
	}
	if _, err := repository.Metadata(ctx, "half-built.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("half-built generation became visible: %v", err)
	}

	replacement, err := repository.StartGeneration(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("StartGeneration(replacement) error = %v", err)
	}
	replacementPath, _ := storage.ParsePath("replacement.txt", false)
	replacementEntry, _ := NewEntry(replacementPath, KindFile, 3, now, now, "", nil)
	if err := repository.InsertGenerationBatch(ctx, replacement.ID, []Entry{replacementEntry}); err != nil {
		t.Fatalf("InsertGenerationBatch(replacement) error = %v", err)
	}
	if err := repository.ActivateGeneration(ctx, replacement.ID, 1, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("ActivateGeneration() error = %v", err)
	}
	if _, err := repository.Metadata(ctx, "replacement.txt"); err != nil {
		t.Fatalf("replacement generation not visible: %v", err)
	}
	if _, err := repository.Metadata(ctx, "old.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("old generation still visible: %v", err)
	}
	for {
		deleted, cleanupErr := repository.CleanupObsoleteBatch(ctx, ReindexCleanupBatchSize)
		if cleanupErr != nil {
			t.Fatalf("CleanupObsoleteBatch() error = %v", cleanupErr)
		}
		if deleted == 0 {
			break
		}
	}
	var obsolete int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_index_generations WHERE status = 'obsolete'`).Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatalf("obsolete generations = %d, %v; want 0", obsolete, err)
	}
}

func TestDurableMutationIntentFailsClosedUntilCompletedOrReindexed(t *testing.T) {
	db, repository, now := openIndexTestRepository(t)
	ctx := context.Background()

	if err := repository.BeginMutation(ctx, mkdirMutationReason, now); err != nil {
		t.Fatalf("BeginMutation() error = %v", err)
	}
	if err := repository.CheckHealthy(ctx); !errors.Is(err, ErrIndexInconsistent) {
		t.Fatalf("CheckHealthy(pending mutation) error = %v; want ErrIndexInconsistent", err)
	}
	if err := repository.ClearMutation(ctx, mkdirMutationReason, now.Add(time.Second)); err != nil {
		t.Fatalf("ClearMutation() error = %v", err)
	}
	if err := repository.CheckHealthy(ctx); err != nil {
		t.Fatalf("CheckHealthy(cleared mutation) error = %v", err)
	}

	if err := repository.BeginMutation(ctx, mkdirMutationReason, now.Add(2*time.Second)); err != nil {
		t.Fatalf("BeginMutation(second) error = %v", err)
	}
	logicalPath, _ := storage.ParsePath("durable-intent", false)
	entry, _ := NewEntry(logicalPath, KindDirectory, 0, now, now.Add(2*time.Second), "", nil)
	if err := repository.CreateWithAuditAndRepair(ctx, entry, testAuditEvent("test.durable_create"), mkdirMutationReason); err != nil {
		t.Fatalf("CreateWithAuditAndRepair() error = %v", err)
	}
	if err := repository.CheckHealthy(ctx); err != nil {
		t.Fatalf("CheckHealthy(completed mutation) error = %v", err)
	}

	if err := repository.BeginMutation(ctx, moveMutationReason, now.Add(3*time.Second)); err != nil {
		t.Fatalf("BeginMutation(crash simulation) error = %v", err)
	}
	building, err := repository.StartGeneration(ctx, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("StartGeneration() error = %v", err)
	}
	if err := repository.InsertGenerationBatch(ctx, building.ID, []Entry{entry}); err != nil {
		t.Fatalf("InsertGenerationBatch() error = %v", err)
	}
	if err := repository.ActivateGeneration(ctx, building.ID, 1, now.Add(5*time.Second)); err != nil {
		t.Fatalf("ActivateGeneration() error = %v", err)
	}
	if err := repository.CheckHealthy(ctx); err != nil {
		t.Fatalf("CheckHealthy(after explicit reindex) error = %v", err)
	}

	var healthy int
	var reason sql.NullString
	if err := db.QueryRow(`SELECT healthy, unhealthy_reason FROM file_index_state WHERE singleton = 1`).Scan(&healthy, &reason); err != nil {
		t.Fatal(err)
	}
	if healthy != 1 || reason.Valid {
		t.Fatalf("index state = healthy %d reason %q; want healthy", healthy, reason.String)
	}
}

func TestSQLiteSupportsFTS5ButFileIndexUsesOrdinaryTables(t *testing.T) {
	db, _, _ := openIndexTestRepository(t)
	var enabled, ftsTables int
	if err := db.QueryRow(`SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("SQLite FTS5 support = %d, %v; want enabled", enabled, err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'table' AND sql LIKE '%USING fts5%'
	`).Scan(&ftsTables); err != nil || ftsTables != 0 {
		t.Fatalf("file index FTS5 tables = %d, %v; want ordinary tables only", ftsTables, err)
	}
}

func TestOpaqueCursorIsBoundToQueryScope(t *testing.T) {
	value := encodeCursor(indexCursor{
		Mode: "list", Scope: "docs", Primary: "a.txt", Secondary: "a.txt", Tertiary: "docs/a.txt",
	})
	if _, err := decodeCursor(value, "list", "docs"); err != nil {
		t.Fatalf("decodeCursor(valid) error = %v", err)
	}
	if _, err := decodeCursor(value, "list", "archive"); !errors.Is(err, ErrInvalidPageCursor) {
		t.Fatalf("decodeCursor(cross-directory) error = %v; want ErrInvalidPageCursor", err)
	}
}

func TestIndexSearchHonorsCancellation(t *testing.T) {
	_, repository, _ := openIndexTestRepository(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.Search(ctx, "anything", 10, indexCursor{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search(cancelled) error = %v; want context.Canceled", err)
	}
}

func TestSQLiteFileIndexTenThousandEntriesAndDirectoryMove(t *testing.T) {
	db, repository, now := openIndexTestRepository(t)
	ctx := context.Background()
	generation, err := repository.StartGeneration(ctx, now)
	if err != nil {
		t.Fatalf("StartGeneration() error = %v", err)
	}
	entries := make([]Entry, 0, ReindexBatchSize)
	appendEntry := func(value string, kind Kind, size int64) {
		logicalPath, parseErr := storage.ParsePath(value, false)
		if parseErr != nil {
			t.Fatalf("ParsePath(%q): %v", value, parseErr)
		}
		entry, entryErr := NewEntry(logicalPath, kind, size, now, now, "", nil)
		if entryErr != nil {
			t.Fatalf("NewEntry(%q): %v", value, entryErr)
		}
		entries = append(entries, entry)
		if len(entries) == ReindexBatchSize {
			if insertErr := repository.InsertGenerationBatch(ctx, generation.ID, entries); insertErr != nil {
				t.Fatalf("InsertGenerationBatch(): %v", insertErr)
			}
			entries = entries[:0]
		}
	}
	appendEntry("bulk", KindDirectory, 0)
	for index := 0; index < 9_999; index++ {
		appendEntry(fmt.Sprintf("bulk/file-%05d.txt", index), KindFile, int64(index))
	}
	if len(entries) != 0 {
		if err := repository.InsertGenerationBatch(ctx, generation.ID, entries); err != nil {
			t.Fatalf("InsertGenerationBatch(final): %v", err)
		}
	}
	if err := repository.ActivateGeneration(ctx, generation.ID, 10_000, now); err != nil {
		t.Fatalf("ActivateGeneration() error = %v", err)
	}

	page, err := repository.List(ctx, "bulk", 501, indexCursor{})
	if err != nil || len(page) != 501 || page[0].Name != "file-00000.txt" {
		t.Fatalf("List(10k) count = %d, first = %+v, error = %v", len(page), page[0], err)
	}
	entry, err := repository.Metadata(ctx, "bulk/file-05000.txt")
	if err != nil || entry.Size != 5000 {
		t.Fatalf("Metadata(10k) = %+v, %v", entry, err)
	}
	search, err := repository.Search(ctx, "file-09998", 10, indexCursor{})
	if err != nil || len(search) != 1 || search[0].Path != "bulk/file-09998.txt" {
		t.Fatalf("Search(10k) = %+v, %v", search, err)
	}
	storageGuard := &failOnMetadataStorage{t: t}
	service := NewService(storageGuard, &emptyTrashRepository{}, repository, storage.NewMutationCoordinator(), audit.NewService(audit.NewSQLiteRepository(db), nil), DefaultConcurrentDownloads, nil)
	identity := auth.Identity{User: auth.User{ID: 1, Role: auth.RoleOwner}}
	listed := 0
	previousPath := ""
	cursor := ""
	for {
		page, listErr := service.List(ctx, identity, "bulk", MaximumListLimit, cursor)
		if listErr != nil {
			t.Fatalf("Service.List(10k page) error = %v", listErr)
		}
		for _, item := range page.Entries {
			if previousPath != "" && item.Path <= previousPath {
				t.Fatalf("pagination order regressed from %q to %q", previousPath, item.Path)
			}
			previousPath = item.Path
			listed++
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if listed != 9_999 {
		t.Fatalf("paginated list entries = %d; want 9999", listed)
	}
	searched := 0
	cursor = ""
	for {
		page, searchErr := service.Search(ctx, identity, "bulk/file-", MaximumSearchLimit, cursor)
		if searchErr != nil {
			t.Fatalf("Service.Search(10k page) error = %v", searchErr)
		}
		searched += len(page.Entries)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if searched != 9_999 || storageGuard.calls != 0 {
		t.Fatalf("paginated search entries = %d, storage calls = %d; want 9999,0", searched, storageGuard.calls)
	}
	source, _ := storage.ParsePath("bulk", false)
	destination, _ := storage.ParsePath("archive", false)
	if err := repository.MoveSubtreeWithAudit(ctx, source, destination, testAuditEvent("test.move")); err != nil {
		t.Fatalf("MoveSubtreeWithAudit(10k) error = %v", err)
	}
	if _, err := repository.Metadata(ctx, "archive/file-05000.txt"); err != nil {
		t.Fatalf("moved descendant metadata error = %v", err)
	}
}

func TestMetadataPlaneReadsNeverCallStorage(t *testing.T) {
	_, repository, now := openIndexTestRepository(t)
	createIndexedEntry(t, repository, "indexed.txt", KindFile, 7, now)
	storageGuard := &failOnMetadataStorage{t: t}
	service := NewService(storageGuard, &emptyTrashRepository{}, repository, storage.NewMutationCoordinator(), audit.NewService(audit.NewSQLiteRepository(repository.db), nil), DefaultConcurrentDownloads, nil)
	identity := auth.Identity{User: auth.User{ID: 1, Role: auth.RoleOwner}}
	ctx := context.Background()
	if _, err := service.List(ctx, identity, "", 10, ""); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if _, err := service.Metadata(ctx, identity, "indexed.txt"); err != nil {
		t.Fatalf("Metadata() error = %v", err)
	}
	if _, err := service.Metadata(ctx, identity, ""); err != nil {
		t.Fatalf("Metadata(root) error = %v", err)
	}
	if _, err := service.Search(ctx, identity, "indexed", 10, ""); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if _, err := service.ListTrash(ctx, identity, 10); err != nil {
		t.Fatalf("ListTrash() error = %v", err)
	}
	if storageGuard.calls != 0 {
		t.Fatalf("metadata plane touched storage %d times", storageGuard.calls)
	}
}

func BenchmarkSQLiteFileIndexQueries10K(b *testing.B) {
	_, repository, now := openIndexBenchmarkRepository(b)
	generation, err := repository.StartGeneration(context.Background(), now)
	if err != nil {
		b.Fatal(err)
	}
	batch := make([]Entry, 0, ReindexBatchSize)
	directoryPath, _ := storage.ParsePath("bench", false)
	directory, _ := NewEntry(directoryPath, KindDirectory, 0, now, now, "", nil)
	batch = append(batch, directory)
	for index := 0; index < 9_999; index++ {
		logicalPath, _ := storage.ParsePath(fmt.Sprintf("bench/file-%05d.txt", index), false)
		entry, _ := NewEntry(logicalPath, KindFile, int64(index), now, now, "", nil)
		batch = append(batch, entry)
		if len(batch) == ReindexBatchSize {
			if err := repository.InsertGenerationBatch(context.Background(), generation.ID, batch); err != nil {
				b.Fatal(err)
			}
			batch = batch[:0]
		}
	}
	if err := repository.ActivateGeneration(context.Background(), generation.ID, 10_000, now); err != nil {
		b.Fatal(err)
	}
	b.Run("directory-list-100", func(b *testing.B) {
		for range b.N {
			if _, err := repository.List(context.Background(), "bench", 100, indexCursor{}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("metadata-lookup", func(b *testing.B) {
		for range b.N {
			if _, err := repository.Metadata(context.Background(), "bench/file-05000.txt"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("filename-search", func(b *testing.B) {
		for range b.N {
			if _, err := repository.Search(context.Background(), "file-09998", 50, indexCursor{}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("directory-move-subtree", func(b *testing.B) {
		source, _ := storage.ParsePath("bench", false)
		destination, _ := storage.ParsePath("archive", false)
		if _, err := repository.Metadata(context.Background(), source.String()); errors.Is(err, storage.ErrNotFound) {
			source, destination = destination, source
		} else if err != nil {
			b.Fatal(err)
		}
		for range b.N {
			if err := repository.MoveSubtreeWithAudit(context.Background(), source, destination, testAuditEvent("benchmark.move")); err != nil {
				b.Fatal(err)
			}
			source, destination = destination, source
		}
	})
}

func openIndexTestRepository(t *testing.T) (*sql.DB, *SQLiteFileIndexRepository, time.Time) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("database.Migrate() error = %v", err)
	}
	return db, NewSQLiteFileIndexRepository(db), time.Unix(1_800_000_000, 0).UTC()
}

func openIndexBenchmarkRepository(b *testing.B) (*sql.DB, *SQLiteFileIndexRepository, time.Time) {
	b.Helper()
	db, err := database.Open(context.Background(), filepath.Join(b.TempDir(), "index.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db); err != nil {
		b.Fatal(err)
	}
	return db, NewSQLiteFileIndexRepository(db), time.Unix(1_800_000_000, 0).UTC()
}

func createIndexedEntry(t *testing.T, repository *SQLiteFileIndexRepository, value string, kind Kind, size int64, now time.Time) Entry {
	t.Helper()
	logicalPath, err := storage.ParsePath(value, false)
	if err != nil {
		t.Fatalf("ParsePath(%q) error = %v", value, err)
	}
	entry, err := NewEntry(logicalPath, kind, size, now, now, "", nil)
	if err != nil {
		t.Fatalf("NewEntry(%q) error = %v", value, err)
	}
	if err := repository.CreateWithAudit(context.Background(), entry, testAuditEvent("test.create")); err != nil {
		t.Fatalf("CreateWithAudit(%q) error = %v", value, err)
	}
	return entry
}

func testAuditEvent(eventType string) audit.Event {
	return audit.Event{Type: eventType, Outcome: audit.OutcomeSuccess}
}

type failOnMetadataStorage struct {
	t     *testing.T
	calls int
}

func (storageGuard *failOnMetadataStorage) called() error {
	storageGuard.calls++
	storageGuard.t.Helper()
	storageGuard.t.Error("metadata plane called physical storage")
	return errors.New("unexpected storage call")
}

func (storageGuard *failOnMetadataStorage) CreateDirectory(storage.Path) error {
	return storageGuard.called()
}
func (storageGuard *failOnMetadataStorage) RemoveEmptyDirectory(storage.Path) error {
	return storageGuard.called()
}
func (storageGuard *failOnMetadataStorage) Move(storage.Path, storage.Path) error {
	return storageGuard.called()
}
func (storageGuard *failOnMetadataStorage) MoveToTrash(storage.Path, string) error {
	return storageGuard.called()
}
func (storageGuard *failOnMetadataStorage) RestoreFromTrash(string, storage.Path) error {
	return storageGuard.called()
}
func (storageGuard *failOnMetadataStorage) TrashState(string, storage.Path) (bool, bool, error) {
	return false, false, storageGuard.called()
}
func (storageGuard *failOnMetadataStorage) OpenDownload(storage.Path) (*os.File, storage.Entry, error) {
	return nil, storage.Entry{}, storageGuard.called()
}

type emptyTrashRepository struct{}

func (*emptyTrashRepository) PrepareTrash(context.Context, TrashEntry) error  { return nil }
func (*emptyTrashRepository) AbortTrash(context.Context, int64, string) error { return nil }
func (*emptyTrashRepository) CommitTrashWithAudit(context.Context, int64, string, time.Time, audit.Event) error {
	return nil
}
func (*emptyTrashRepository) List(context.Context, int64, int) ([]TrashEntry, error) { return nil, nil }
func (*emptyTrashRepository) BeginRestore(context.Context, int64, string, time.Time) (TrashEntry, error) {
	return TrashEntry{}, nil
}
func (*emptyTrashRepository) RollbackRestore(context.Context, int64, string, time.Time) error {
	return nil
}
func (*emptyTrashRepository) FinishRestoreWithAudit(context.Context, int64, string, audit.Event) error {
	return nil
}
func (*emptyTrashRepository) ListReconciliation(context.Context, int) ([]TrashEntry, error) {
	return nil, nil
}
