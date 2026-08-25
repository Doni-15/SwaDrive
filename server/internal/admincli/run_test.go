package admincli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

func TestBootstrapOwnerUsesInteractivePasswordAndCreatesAuditEvent(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "admin.db")
	password := "a sufficiently long passphrase"
	passwords := []string{password, password}
	readIndex := 0
	reader := func(string) (string, error) {
		value := passwords[readIndex]
		readIndex++
		return value, nil
	}
	var stdout, stderr bytes.Buffer

	err := Run(
		context.Background(),
		[]string{"bootstrap-owner", "-database", databasePath, "-username", " Owner.Name "},
		&stdout,
		&stderr,
		reader,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if readIndex != 2 {
		t.Fatalf("password reads = %d; want 2", readIndex)
	}
	if strings.Contains(stdout.String(), password) || strings.Contains(stderr.String(), password) {
		t.Fatal("CLI output contains plaintext password")
	}
	if !strings.Contains(stdout.String(), `Owner "owner.name" created.`) {
		t.Fatalf("stdout = %q; want canonical owner confirmation", stdout.String())
	}

	db := openAdminTestDatabase(t, databasePath)
	var username, passwordHash string
	if err := db.QueryRow(`SELECT username, password_hash FROM users WHERE role = 'owner'`).Scan(&username, &passwordHash); err != nil {
		t.Fatalf("query owner: %v", err)
	}
	if username != "owner.name" || passwordHash == "" || passwordHash == password {
		t.Fatalf("stored owner = %q, hash length %d; want canonical username and encoded hash", username, len(passwordHash))
	}
	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE event_type = 'auth.owner_bootstrap'`).Scan(&auditCount); err != nil {
		t.Fatalf("count bootstrap events: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("bootstrap audit count = %d; want 1", auditCount)
	}

	readIndex = 0
	err = Run(
		context.Background(),
		[]string{"bootstrap-owner", "-database", databasePath, "-username", "second-owner"},
		&bytes.Buffer{},
		&bytes.Buffer{},
		reader,
	)
	if err == nil {
		t.Fatal("second bootstrap owner succeeded; want refusal")
	}
}

func TestReindexCommandBuildsMetadataWithoutReadingUploads(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "state.db")
	storagePath := filepath.Join(base, "storage")
	if err := os.Mkdir(storagePath, 0o750); err != nil {
		t.Fatal(err)
	}
	manager, err := storage.Open(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}

	const volumeID = "test-volume-reindex"
	if err := os.WriteFile(
		filepath.Join(storagePath, ".swadrive-volume"),
		[]byte(volumeID+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(storagePath, "files", "docs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "files", "docs", "indexed.txt"), []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storagePath, "uploads", "ignored.part"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"reindex", "-database", databasePath, "-storage", storagePath, "-volume-id", volumeID}, &stdout, &stderr, nil); err != nil {
		t.Fatalf("Run(reindex) error = %v, stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Reindex complete:") || strings.Contains(stdout.String(), "indexed.txt") || strings.Contains(stdout.String(), storagePath) {
		t.Fatalf("unsafe or missing reindex progress output: %q", stdout.String())
	}
	db := openAdminTestDatabase(t, databasePath)
	var visible, uploads int
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE logical_path = 'docs/indexed.txt' AND trash_entry_id IS NULL`).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE logical_path LIKE '%ignored%'`).Scan(&uploads); err != nil {
		t.Fatal(err)
	}
	if visible != 1 || uploads != 0 {
		t.Fatalf("reindex visible=%d uploads=%d; want 1,0", visible, uploads)
	}
}

func TestAdminCommandsRefuseDatabaseOwnedByServerProcess(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "state.db")
	storagePath := filepath.Join(base, "storage")

	if err := os.Mkdir(storagePath, 0o750); err != nil {
		t.Fatal(err)
	}

	lock, err := database.AcquireProcessLock(databasePath)
	if err != nil {
		t.Fatalf("AcquireProcessLock() error = %v", err)
	}
	defer lock.Close()

	passwordRead := false
	reader := func(string) (string, error) {
		passwordRead = true
		return "this password must never be requested", nil
	}

	err = Run(
		context.Background(),
		[]string{"bootstrap-owner", "-database", databasePath, "-username", "owner"},
		&bytes.Buffer{},
		&bytes.Buffer{},
		reader,
	)
	if !errors.Is(err, database.ErrProcessLockBusy) {
		t.Fatalf("bootstrap-owner error = %v; want ErrProcessLockBusy", err)
	}
	if passwordRead {
		t.Fatal("bootstrap-owner requested a password before refusing the process lock")
	}

	err = Run(
		context.Background(),
		[]string{"reindex", "-database", databasePath, "-storage", storagePath, "-volume-id", "test-volume-lock"},
		&bytes.Buffer{},
		&bytes.Buffer{},
		nil,
	)
	if !errors.Is(err, database.ErrProcessLockBusy) {
		t.Fatalf("reindex error = %v; want ErrProcessLockBusy", err)
	}
}

func TestReindexRejectsWrongStorageVolume(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "state.db")
	storagePath := filepath.Join(base, "storage")

	if err := os.Mkdir(storagePath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(storagePath, ".swadrive-volume"),
		[]byte("actual-volume\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := Run(
		context.Background(),
		[]string{
			"reindex",
			"-database", databasePath,
			"-storage", storagePath,
			"-volume-id", "expected-volume",
		},
		&stdout,
		&stderr,
		nil,
	)
	if !errors.Is(err, storage.ErrStorageVolumeMismatch) {
		t.Fatalf(
			"Run(reindex wrong volume) error = %v; want ErrStorageVolumeMismatch",
			err,
		)
	}

	for _, directory := range []string{"files", "uploads", "trash"} {
		if _, err := os.Stat(filepath.Join(storagePath, directory)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s exists after rejected reindex volume; want absent", directory)
		}
	}
}

func TestReconcileUploadPartsIsExplicitAgeGatedAndPathPrivate(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "state.db")
	storagePath := filepath.Join(base, "storage")
	if err := os.Mkdir(storagePath, 0o750); err != nil {
		t.Fatal(err)
	}
	manager, err := storage.Open(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	const volumeID = "test-volume-orphan-parts"
	if err := os.WriteFile(filepath.Join(storagePath, ".swadrive-volume"), []byte(volumeID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orphanName := "11111111111111111111111111111111.part"
	orphanPath := filepath.Join(storagePath, "uploads", orphanName)
	if err := os.WriteFile(orphanPath, []byte("orphan bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(orphanPath, old, old); err != nil {
		t.Fatal(err)
	}

	arguments := []string{"reconcile-upload-parts", "-database", databasePath, "-storage", storagePath, "-volume-id", volumeID, "-minimum-age", "24h"}
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), arguments, &stdout, &stderr, nil); err != nil {
		t.Fatalf("Run(dry-run) error = %v", err)
	}
	if _, err := os.Stat(orphanPath); err != nil {
		t.Fatalf("dry-run removed orphan: %v", err)
	}
	if !strings.Contains(stdout.String(), "dry-run: scanned=1 orphans=1 removed=0") || strings.Contains(stdout.String(), orphanName) || strings.Contains(stdout.String(), storagePath) {
		t.Fatalf("dry-run output is unsafe or incomplete: %q", stdout.String())
	}

	stdout.Reset()
	arguments = append(arguments, "-apply")
	if err := Run(context.Background(), arguments, &stdout, &stderr, nil); err != nil {
		t.Fatalf("Run(apply) error = %v", err)
	}
	if _, err := os.Stat(orphanPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan still exists after apply: %v", err)
	}
	if !strings.Contains(stdout.String(), "applied: scanned=1 orphans=1 removed=1") || strings.Contains(stdout.String(), orphanName) || strings.Contains(stdout.String(), storagePath) {
		t.Fatalf("apply output is unsafe or incomplete: %q", stdout.String())
	}
}

func openAdminTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
