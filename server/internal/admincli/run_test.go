package admincli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if err := Run(context.Background(), []string{"reindex", "-database", databasePath, "-storage", storagePath}, &stdout, &stderr, nil); err != nil {
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

func openAdminTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
