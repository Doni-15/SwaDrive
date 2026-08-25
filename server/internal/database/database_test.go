package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOpenRequiresPath(t *testing.T) {
	_, err := Open(context.Background(), "  ")
	if !errors.Is(err, ErrDatabasePathRequired) {
		t.Fatalf("Open() error = %v; want ErrDatabasePathRequired", err)
	}
}

func TestOpenInitializesSQLiteConnections(t *testing.T) {
	db := openTestDatabase(t)

	assertPragma(t, db, "foreign_keys", "1")
	assertPragma(t, db, "busy_timeout", busyTimeoutMillis)
	assertPragma(t, db, "journal_mode", "wal")
	assertPragma(t, db, "synchronous", "2")
}

func TestMigrateCreatesAuthenticationSchemaAndIsRepeatable(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()

	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("first Migrate() error = %v", err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}

	var migrationCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrationCount != 6 {
		t.Fatalf("migration count = %d; want 6", migrationCount)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT name
		FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}

	wantTables := []string{
		"audit_events",
		"file_entries",
		"file_index_generations",
		"file_index_state",
		"schema_migrations",
		"sessions",
		"trash_entries",
		"upload_chunks",
		"uploads",
		"users",
	}
	if !reflect.DeepEqual(tables, wantTables) {
		t.Fatalf("tables = %v; want %v", tables, wantTables)
	}
}

func TestBackendV1MigrationConstraintsAndIndexes(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var triggerCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'trigger' AND name IN ('audit_events_reject_update', 'audit_events_reject_delete')
	`).Scan(&triggerCount); err != nil {
		t.Fatalf("count audit triggers: %v", err)
	}
	if triggerCount != 2 {
		t.Fatalf("audit trigger count = %d; want 2", triggerCount)
	}

	var indexCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'index' AND name IN (
			'audit_events_occurred_idx', 'audit_events_type_idx',
			'audit_events_actor_idx', 'audit_events_outcome_idx',
			'trash_entries_user_time_idx', 'uploads_user_time_idx', 'uploads_cleanup_idx'
		)
	`).Scan(&indexCount); err != nil {
		t.Fatalf("count backend-v1 indexes: %v", err)
	}
	if indexCount != 7 {
		t.Fatalf("backend-v1 index count = %d; want 7", indexCount)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO uploads (
			id, user_id, target_path, part_name, total_size, chunk_size,
			total_chunks, status, created_at, updated_at, expires_at
		) VALUES ('upload-id-123456', 999, 'file.bin', 'upload.part', 1, 4194304, 1, 'pending', 1, 1, 2)
	`); err == nil {
		t.Fatal("upload with missing user insert succeeded; want foreign-key error")
	}

	result, err := db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES ('migration-owner', 'encoded-hash', 'owner', 1, 1)
	`)
	if err != nil {
		t.Fatalf("insert migration constraint user: %v", err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read migration constraint user ID: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO uploads (
			id, user_id, target_path, part_name, total_size, chunk_size,
			total_chunks, status, created_at, updated_at, expires_at
		) VALUES ('invalid-chunks-1', ?, 'file.bin', 'invalid.part', 4194305, 4194304, 1, 'pending', 1, 1, 2)
	`, userID); err == nil {
		t.Fatal("upload with inconsistent total_chunks succeeded; want check constraint error")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO audit_events (occurred_at, event_type, outcome, metadata_json)
		VALUES (1, 'test.invalid_metadata', 'failure', 'not-json')
	`); err == nil {
		t.Fatal("audit event with invalid metadata JSON succeeded; want check constraint error")
	}
}

func TestFileIndexAndAuditLogicalPathMigrationConstraints(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	var activeGeneration, healthy int64
	if err := db.QueryRowContext(ctx, `SELECT active_generation_id, healthy FROM file_index_state WHERE singleton = 1`).Scan(&activeGeneration, &healthy); err != nil || activeGeneration != 1 || healthy != 1 {
		t.Fatalf("initial index state = generation %d healthy %d, %v", activeGeneration, healthy, err)
	}
	insertEntry := func(trash any) error {
		_, err := db.ExecContext(ctx, `
			INSERT INTO file_entries (
				generation_id, logical_path, parent_path, name, normalized_name,
				normalized_path, kind, size, modified_at, indexed_at, trash_entry_id
			) VALUES (1, 'docs/a.txt', 'docs', 'a.txt', 'a.txt', 'docs/a.txt', 'file', 1, 1, 1, ?)
		`, trash)
		return err
	}
	if err := insertEntry(nil); err != nil {
		t.Fatalf("insert active file index entry: %v", err)
	}
	if err := insertEntry(nil); err == nil {
		t.Fatal("duplicate active path succeeded")
	}
	if _, err := db.ExecContext(ctx, `UPDATE file_entries SET trash_entry_id = '0123456789abcdef' WHERE logical_path = 'docs/a.txt'`); err != nil {
		t.Fatalf("mark old path trashed: %v", err)
	}
	if err := insertEntry(nil); err != nil {
		t.Fatalf("active path alongside trashed old path error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO audit_events (occurred_at, event_type, outcome, resource_path, destination_path)
		VALUES (1, 'test.logical_paths', 'success', 'docs/a.txt', 'archive/a.txt')
	`); err != nil {
		t.Fatalf("insert bounded logical audit paths: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO audit_events (occurred_at, event_type, outcome, resource_path)
		VALUES (1, 'test.long_path', 'success', ?)
	`, strings.Repeat("a", 4097)); err == nil {
		t.Fatal("oversized audit resource path succeeded")
	}
}

func TestAuthenticationSchemaConstraints(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	result, err := db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, "alice", "encoded-hash", "owner", 1_700_000_000, 1_700_000_000)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read user ID: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, "ALICE", "another-hash", "member", 1_700_000_000, 1_700_000_000); err == nil {
		t.Fatal("case-insensitive duplicate username insert succeeded; want constraint error")
	}

	tokenHash := make([]byte, 32)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (
			user_id, token_hash, client_name, created_at, expires_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, userID, tokenHash, "Linux laptop", 1_700_000_000, 1_700_086_400, 1_700_000_000); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (
			user_id, token_hash, client_name, created_at, expires_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, userID, tokenHash, "Android phone", 1_700_000_000, 1_700_086_400, 1_700_000_000); err == nil {
		t.Fatal("duplicate token hash insert succeeded; want constraint error")
	}

	anotherTokenHash := make([]byte, 32)
	anotherTokenHash[0] = 1
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (
			user_id, token_hash, client_name, created_at, expires_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, userID+999, anotherTokenHash, "Unknown device", 1_700_000_000, 1_700_086_400, 1_700_000_000); err == nil {
		t.Fatal("session with missing user insert succeeded; want foreign-key error")
	}
}

func TestMigrateRejectsChangedAppliedMigration(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET checksum = 'changed' WHERE version = 1`); err != nil {
		t.Fatalf("change migration checksum: %v", err)
	}

	err := Migrate(ctx, db)
	if !errors.Is(err, ErrMigrationMismatch) {
		t.Fatalf("Migrate() error = %v; want ErrMigrationMismatch", err)
	}
}

func TestValidateAppliedMigrationsRequiresPrefix(t *testing.T) {
	migrations := []migration{
		{version: 10, name: "0010_auth.sql", checksum: "auth-checksum"},
		{version: 20, name: "0020_sessions.sql", checksum: "sessions-checksum"},
		{version: 30, name: "0030_future.sql", checksum: "future-checksum"},
	}

	tests := []struct {
		name    string
		applied map[int64]appliedMigration
		wantErr bool
	}{
		{
			name: "non-consecutive version prefix",
			applied: map[int64]appliedMigration{
				10: {name: "0010_auth.sql", checksum: "auth-checksum"},
				20: {name: "0020_sessions.sql", checksum: "sessions-checksum"},
			},
		},
		{
			name: "missing earlier migration",
			applied: map[int64]appliedMigration{
				20: {name: "0020_sessions.sql", checksum: "sessions-checksum"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAppliedMigrations(tt.applied, migrations)
			if tt.wantErr && !errors.Is(err, ErrMigrationMismatch) {
				t.Fatalf("validateAppliedMigrations() error = %v; want ErrMigrationMismatch", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateAppliedMigrations() error = %v; want nil", err)
			}
		})
	}
}

func TestAuthenticationSchemaRejectsPaddedNames(t *testing.T) {
	db := openTestDatabase(t)
	ctx := context.Background()
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, " padded-user ", "encoded-hash", "member", 1_700_000_000, 1_700_000_000); err == nil {
		t.Fatal("padded username insert succeeded; want constraint error")
	}

	result, err := db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, "alice", "encoded-hash", "owner", 1_700_000_000, 1_700_000_000)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read user ID: %v", err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (
			user_id, token_hash, client_name, created_at, expires_at, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, userID, make([]byte, 32), " padded-client ", 1_700_000_000, 1_700_086_400, 1_700_000_000); err == nil {
		t.Fatal("padded client name insert succeeded; want constraint error")
	}
}

func openTestDatabase(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "swadrive-test.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return db
}

func assertPragma(t *testing.T, db *sql.DB, name, want string) {
	t.Helper()

	var got string
	if err := db.QueryRow("PRAGMA " + name).Scan(&got); err != nil {
		t.Fatalf("read PRAGMA %s: %v", name, err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %q; want %q", name, got, want)
	}
}
