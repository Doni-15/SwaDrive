package database_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/files"
)

func TestSQLiteConcurrentSessionReadsAndAuditWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "concurrent.db"))
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatalf("database.Migrate() error = %v", err)
	}

	now := time.Unix(1_800_000_000, 0).UTC()
	userResult, err := db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		VALUES ('owner', 'test-only-hash', 'owner', ?, ?)
	`, now.Unix(), now.Unix())
	if err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	userID, _ := userResult.LastInsertId()
	_, hashedToken, err := auth.GenerateSessionToken()
	if err != nil {
		t.Fatalf("auth.GenerateSessionToken() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (user_id, token_hash, client_name, created_at, expires_at, last_seen_at)
		VALUES (?, ?, 'concurrent-device', ?, ?, ?)
	`, userID, hashedToken.Bytes(), now.Unix(), now.Add(time.Hour).Unix(), now.Unix()); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	authRepository := auth.NewSQLiteRepository(db)
	auditService := audit.NewService(audit.NewSQLiteRepository(db), func() time.Time { return now })
	const readers = 8
	const operations = 100
	for operation := range operations {
		logicalPath := fmt.Sprintf("file-%d", operation)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO file_entries (
				generation_id, logical_path, parent_path, name, normalized_name,
				normalized_path, kind, size, modified_at, indexed_at
			) VALUES (1, ?, '', ?, ?, ?, 'file', 0, ?, ?)
		`, logicalPath, logicalPath, logicalPath, logicalPath, now.Unix(), now.Unix()); err != nil {
			t.Fatalf("insert indexed file %d: %v", operation, err)
		}
	}
	errorsSeen := make(chan error, readers+2)
	var wait sync.WaitGroup
	for reader := range readers {
		wait.Add(1)
		go func(reader int) {
			defer wait.Done()
			for operation := range operations {
				session, findErr := authRepository.FindAuthenticatedSession(ctx, hashedToken)
				if findErr != nil {
					errorsSeen <- fmt.Errorf("reader %d operation %d: %w", reader, operation, findErr)
					return
				}
				if session.User.ID != userID {
					errorsSeen <- fmt.Errorf("reader %d got user %d", reader, session.User.ID)
					return
				}
			}
		}(reader)
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		for operation := range operations {
			if recordErr := auditService.Record(ctx, audit.Event{
				OccurredAt: now.Add(time.Duration(operation) * time.Second),
				Type:       audit.EventLoginFailure,
				Outcome:    audit.OutcomeFailure,
				RemoteIP:   "192.0.2.1",
			}); recordErr != nil {
				errorsSeen <- fmt.Errorf("writer operation %d: %w", operation, recordErr)
				return
			}
		}
	}()
	wait.Add(1)
	go func() {
		defer wait.Done()
		repository := files.NewSQLiteTrashRepository(db)
		for operation := range operations {
			id := fmt.Sprintf("%032x", operation+1)
			entry := files.TrashEntry{
				ID: id, UserID: userID, OriginalPath: fmt.Sprintf("file-%d", operation), TrashName: id,
				TrashedAt: now, State: files.TrashStateTrashing, UpdatedAt: now,
			}
			if prepareErr := repository.PrepareTrash(ctx, entry); prepareErr != nil {
				errorsSeen <- fmt.Errorf("file writer prepare %d: %w", operation, prepareErr)
				return
			}
			if abortErr := repository.AbortTrash(ctx, userID, id); abortErr != nil {
				errorsSeen <- fmt.Errorf("file writer abort %d: %w", operation, abortErr)
				return
			}
		}
	}()
	wait.Wait()
	close(errorsSeen)
	for concurrentErr := range errorsSeen {
		t.Error(concurrentErr)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("concurrent SQLite workload timed out: %v", err)
	}
	var auditCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&auditCount); err != nil {
		t.Fatalf("count audit events: %v", err)
	}
	if auditCount != operations {
		t.Fatalf("audit event count = %d; want %d", auditCount, operations)
	}
	var trashCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trash_entries`).Scan(&trashCount); err != nil || trashCount != 0 {
		t.Fatalf("trash entry count = %d, %v; want 0", trashCount, err)
	}
}
