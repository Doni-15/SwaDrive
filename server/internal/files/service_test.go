package files

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

func TestFileMutationAuditFailuresCompensateOrReconcile(t *testing.T) {
	service, manager, db, identity, root := newFilesTestService(t)
	ctx := context.Background()

	installFileAuditFailureTrigger(t, db)
	if _, err := service.CreateDirectory(ctx, identity, "temporary-folder"); err == nil {
		t.Fatal("CreateDirectory() succeeded with forced audit failure")
	}
	if _, err := os.Stat(filepath.Join(root, "files", "temporary-folder")); !os.IsNotExist(err) {
		t.Fatalf("folder compensation left directory behind: %v", err)
	}
	var indexedRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE logical_path = 'temporary-folder'`).Scan(&indexedRows); err != nil || indexedRows != 0 {
		t.Fatalf("failed folder index rows = %d, %v; want 0", indexedRows, err)
	}

	writeVisibleFile(t, root, "move-source.txt", []byte("source"))
	reindexFiles(t, db, manager)
	if err := service.Move(ctx, identity, "move-source.txt", "move-destination.txt"); err == nil {
		t.Fatal("Move() succeeded with forced audit failure")
	}
	if contents, err := os.ReadFile(filepath.Join(root, "files", "move-source.txt")); err != nil || string(contents) != "source" {
		t.Fatalf("move compensation source = %q, %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(root, "files", "move-destination.txt")); !os.IsNotExist(err) {
		t.Fatalf("move compensation left destination behind: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE logical_path = 'move-source.txt' AND trash_entry_id IS NULL`).Scan(&indexedRows); err != nil || indexedRows != 1 {
		t.Fatalf("compensated move source index rows = %d, %v; want 1", indexedRows, err)
	}
	dropFileAuditFailureTrigger(t, db)

	writeVisibleFile(t, root, "trash-source.txt", []byte("trash content"))
	reindexFiles(t, db, manager)
	installFileAuditFailureTrigger(t, db)
	entry, err := service.Trash(ctx, identity, "trash-source.txt")
	if err == nil {
		t.Fatal("Trash() succeeded with forced audit failure")
	}
	if entry.ID != "" {
		t.Fatal("failed Trash() returned a public trash entry")
	}
	var trashID, trashName, state string
	if err := db.QueryRow(`SELECT id, trash_name, state FROM trash_entries`).Scan(&trashID, &trashName, &state); err != nil {
		t.Fatalf("query interrupted trash entry: %v", err)
	}
	if state != string(TrashStateTrashing) {
		t.Fatalf("interrupted trash state = %q; want trashing", state)
	}
	if _, err := os.Stat(filepath.Join(root, "files", "trash-source.txt")); !os.IsNotExist(err) {
		t.Fatalf("interrupted trash source still visible: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "trash", trashName)); err != nil {
		t.Fatalf("interrupted trash payload missing: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE logical_path = 'trash-source.txt' AND trash_entry_id IS NULL`).Scan(&indexedRows); err != nil || indexedRows != 1 {
		t.Fatalf("interrupted trash active index rows = %d, %v; want 1 until reconciliation", indexedRows, err)
	}
	dropFileAuditFailureTrigger(t, db)

	if count, err := service.ReconcileTrash(ctx); err != nil || count != 1 {
		t.Fatalf("ReconcileTrash(trashing) = %d, %v; want 1", count, err)
	}
	if err := db.QueryRow(`SELECT state FROM trash_entries WHERE id = ?`, trashID).Scan(&state); err != nil || state != string(TrashStateTrashed) {
		t.Fatalf("reconciled trash state = %q, %v; want trashed", state, err)
	}

	installFileAuditFailureTrigger(t, db)
	if err := service.Restore(ctx, identity, trashID); err == nil {
		t.Fatal("Restore() succeeded with forced audit failure")
	}
	if err := db.QueryRow(`SELECT state FROM trash_entries WHERE id = ?`, trashID).Scan(&state); err != nil || state != string(TrashStateRestoring) {
		t.Fatalf("interrupted restore state = %q, %v; want restoring", state, err)
	}
	if contents, err := os.ReadFile(filepath.Join(root, "files", "trash-source.txt")); err != nil || string(contents) != "trash content" {
		t.Fatalf("interrupted restore destination = %q, %v", contents, err)
	}
	dropFileAuditFailureTrigger(t, db)

	if count, err := service.ReconcileTrash(ctx); err != nil || count != 1 {
		t.Fatalf("ReconcileTrash(restoring) = %d, %v; want 1", count, err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM trash_entries WHERE id = ?`, trashID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("restored reconciliation rows = %d, %v; want 0", remaining, err)
	}
	if _, err := service.Metadata(ctx, identity, "trash-source.txt"); err != nil {
		t.Fatalf("restored visible file metadata error = %v", err)
	}
}

func newFilesTestService(t *testing.T) (*Service, *storage.Manager, *sql.DB, auth.Identity, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "storage")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatalf("create storage root: %v", err)
	}
	manager, err := storage.Open(root)
	if err != nil {
		t.Fatalf("storage.Open() error = %v", err)
	}
	db, err := database.Open(context.Background(), filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatalf("database.Migrate() error = %v", err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	userResult, err := db.Exec(`
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES ('owner', 'test-only-hash', 'owner', ?, ?)
	`, now.Unix(), now.Unix())
	if err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	userID, _ := userResult.LastInsertId()
	sessionResult, err := db.Exec(`
		INSERT INTO sessions (user_id, token_hash, client_name, created_at, expires_at, last_seen_at)
		VALUES (?, randomblob(32), 'test-device', ?, ?, ?)
	`, userID, now.Unix(), now.Add(time.Hour).Unix(), now.Unix())
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	sessionID, _ := sessionResult.LastInsertId()
	identity := auth.Identity{
		User:      auth.User{ID: userID, Username: "owner", Role: auth.RoleOwner},
		Session:   auth.Session{ID: sessionID, UserID: userID},
		RequestID: "request", RemoteIP: "192.0.2.1",
	}
	auditService := audit.NewService(audit.NewSQLiteRepository(db), func() time.Time { return now })
	indexRepository := NewSQLiteFileIndexRepository(db)
	service := NewService(manager, NewSQLiteTrashRepository(db), indexRepository, storage.NewMutationCoordinator(), auditService, DefaultConcurrentDownloads, func() time.Time { return now })
	t.Cleanup(func() {
		_ = manager.Close()
		_ = db.Close()
	})
	return service, manager, db, identity, root
}

func installFileAuditFailureTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		CREATE TRIGGER force_file_audit_failure
		BEFORE INSERT ON audit_events
		BEGIN
			SELECT RAISE(ABORT, 'forced audit failure');
		END
	`); err != nil {
		t.Fatalf("create forced audit failure trigger: %v", err)
	}
}

func dropFileAuditFailureTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP TRIGGER force_file_audit_failure`); err != nil {
		t.Fatalf("drop forced audit failure trigger: %v", err)
	}
}

func writeVisibleFile(t *testing.T, root, name string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "files", name), contents, 0o600); err != nil {
		t.Fatalf("write visible file: %v", err)
	}
}

func reindexFiles(t *testing.T, db *sql.DB, manager *storage.Manager) {
	t.Helper()
	if _, err := NewRebuilder(NewSQLiteFileIndexRepository(db), manager, nil).Rebuild(context.Background(), nil); err != nil {
		t.Fatalf("Rebuild() error = %v", err)
	}
}

func mustStoragePath(t *testing.T, value string) storage.Path {
	t.Helper()
	path, err := storage.ParsePath(value, false)
	if err != nil {
		t.Fatalf("storage.ParsePath(%q) error = %v", value, err)
	}
	return path
}

func TestReconcileTrashRejectsAmbiguousState(t *testing.T) {
	service, manager, db, identity, root := newFilesTestService(t)
	writeVisibleFile(t, root, "ambiguous.txt", []byte("visible"))
	reindexFiles(t, db, manager)
	now := time.Unix(1_800_000_000, 0).UTC()
	entry := TrashEntry{
		ID: "0123456789abcdef0123456789abcdef", UserID: identity.User.ID,
		OriginalPath: "ambiguous.txt", TrashName: "0123456789abcdef0123456789abcdef",
		TrashedAt: now, UpdatedAt: now, State: TrashStateTrashing,
	}
	if err := service.trash.PrepareTrash(context.Background(), entry); err != nil {
		t.Fatalf("PrepareTrash() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "trash", entry.TrashName), []byte("trash copy"), 0o600); err != nil {
		t.Fatalf("write ambiguous trash copy: %v", err)
	}
	if _, err := service.ReconcileTrash(context.Background()); !errors.Is(err, ErrReconciliation) {
		t.Fatalf("ReconcileTrash(ambiguous) error = %v; want ErrReconciliation", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM trash_entries WHERE id = ?`, entry.ID).Scan(&state); err != nil || state != string(TrashStateTrashing) {
		t.Fatalf("ambiguous state changed to %q, %v", state, err)
	}
}

func TestDownloadConcurrencyGateReleasesOnCloseAndHonorsCancellation(t *testing.T) {
	service, manager, db, identity, root := newFilesTestService(t)
	service.downloadSlots = make(chan struct{}, 1)
	writeVisibleFile(t, root, "download.bin", []byte("content"))
	reindexFiles(t, db, manager)
	first, _, err := service.OpenDownload(context.Background(), identity, "download.bin")
	if err != nil {
		t.Fatalf("OpenDownload(first) error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := service.OpenDownload(cancelled, identity, "download.bin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenDownload(blocked cancelled) error = %v; want context.Canceled", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}
	second, _, err := service.OpenDownload(context.Background(), identity, "download.bin")
	if err != nil {
		t.Fatalf("OpenDownload(after close) error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}
}

func TestDirectoryMoveTrashAndRestoreMaintainIndexedSubtree(t *testing.T) {
	service, manager, db, identity, root := newFilesTestService(t)
	ctx := context.Background()
	if _, err := service.CreateDirectory(ctx, identity, "docs"); err != nil {
		t.Fatalf("CreateDirectory(docs) error = %v", err)
	}
	if _, err := service.CreateDirectory(ctx, identity, "docs/nested"); err != nil {
		t.Fatalf("CreateDirectory(nested) error = %v", err)
	}
	writeVisibleFile(t, root, "docs/nested/file.txt", []byte("indexed"))
	reindexFiles(t, db, manager)
	if err := service.Move(ctx, identity, "docs", "archive"); err != nil {
		t.Fatalf("Move(directory) error = %v", err)
	}
	if _, err := service.Metadata(ctx, identity, "archive/nested/file.txt"); err != nil {
		t.Fatalf("moved descendant metadata error = %v", err)
	}
	if _, err := service.Metadata(ctx, identity, "docs/nested/file.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("old descendant metadata error = %v; want not found", err)
	}

	trashEntry, err := service.Trash(ctx, identity, "archive")
	if err != nil {
		t.Fatalf("Trash(directory) error = %v", err)
	}
	if _, err := service.Metadata(ctx, identity, "archive/nested/file.txt"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("trashed descendant remained visible: %v", err)
	}
	var retained int
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE trash_entry_id = ?`, trashEntry.ID).Scan(&retained); err != nil || retained != 3 {
		t.Fatalf("retained trash subtree rows = %d, %v; want 3", retained, err)
	}
	if err := service.Restore(ctx, identity, trashEntry.ID); err != nil {
		t.Fatalf("Restore(directory) error = %v", err)
	}
	if _, err := service.Metadata(ctx, identity, "archive/nested/file.txt"); err != nil {
		t.Fatalf("restored descendant metadata error = %v", err)
	}
	if err := service.Move(ctx, identity, "archive", "archive/nested"); err == nil {
		t.Fatal("Move() into existing descendant succeeded")
	}
}

func TestFailedMoveCompensationMarksIndexUnhealthy(t *testing.T) {
	_, repository, now := openIndexTestRepository(t)
	createIndexedEntry(t, repository, "source.txt", KindFile, 1, now)
	installFileAuditFailureTrigger(t, repository.db)
	defer dropFileAuditFailureTrigger(t, repository.db)
	storageFailure := &moveCompensationFailureStorage{}
	service := NewService(storageFailure, &emptyTrashRepository{}, repository, storage.NewMutationCoordinator(), audit.NewService(audit.NewSQLiteRepository(repository.db), func() time.Time { return now }), DefaultConcurrentDownloads, func() time.Time { return now })
	identity := auth.Identity{User: auth.User{ID: 1, Role: auth.RoleOwner}}
	err := service.Move(context.Background(), identity, "source.txt", "destination.txt")
	if !errors.Is(err, ErrIndexInconsistent) {
		t.Fatalf("Move() error = %v; want ErrIndexInconsistent", err)
	}
	if _, err := service.Metadata(context.Background(), identity, "source.txt"); !errors.Is(err, ErrIndexInconsistent) {
		t.Fatalf("metadata after failed compensation error = %v; want ErrIndexInconsistent", err)
	}
}

func TestMissingIndexedDownloadFailsClosedWithoutDeletingMetadata(t *testing.T) {
	service, _, db, identity, _ := newFilesTestService(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	createIndexedEntry(t, NewSQLiteFileIndexRepository(db), "missing.bin", KindFile, 10, now)
	if _, _, err := service.OpenDownload(context.Background(), identity, "missing.bin"); !errors.Is(err, ErrIndexInconsistent) {
		t.Fatalf("OpenDownload(missing indexed file) error = %v; want ErrIndexInconsistent", err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE logical_path = 'missing.bin'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("missing file metadata rows = %d, %v; want retained row", rows, err)
	}
	if _, err := service.Metadata(context.Background(), identity, "missing.bin"); !errors.Is(err, ErrIndexInconsistent) {
		t.Fatalf("metadata after discovered mismatch error = %v; want ErrIndexInconsistent", err)
	}
}

type moveCompensationFailureStorage struct {
	moves int
}

func (*moveCompensationFailureStorage) CreateDirectory(storage.Path) error      { return nil }
func (*moveCompensationFailureStorage) RemoveEmptyDirectory(storage.Path) error { return nil }
func (storageFailure *moveCompensationFailureStorage) Move(storage.Path, storage.Path) error {
	storageFailure.moves++
	if storageFailure.moves == 1 {
		return nil
	}
	return errors.New("injected compensation failure")
}
func (*moveCompensationFailureStorage) MoveToTrash(storage.Path, string) error      { return nil }
func (*moveCompensationFailureStorage) RestoreFromTrash(string, storage.Path) error { return nil }
func (*moveCompensationFailureStorage) TrashState(string, storage.Path) (bool, bool, error) {
	return false, false, nil
}
func (*moveCompensationFailureStorage) OpenDownload(storage.Path) (*os.File, storage.Entry, error) {
	return nil, storage.Entry{}, errors.New("not implemented")
}
