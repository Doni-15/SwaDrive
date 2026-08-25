package audit

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/database"
)

func TestAuditEventsAreBoundedOwnerOnlyAndAppendOnly(t *testing.T) {
	db := openAuditTestDatabase(t)
	repository := NewSQLiteRepository(db)
	now := time.Unix(1_800_000_000, 0).UTC()
	service := NewService(repository, func() time.Time { return now })
	ctx := context.Background()

	if err := service.Record(ctx, Event{
		Type:     EventLoginFailure,
		Outcome:  OutcomeFailure,
		Metadata: map[string]string{"raw_token": "must-not-be-accepted"},
	}); !errors.Is(err, ErrUnsafeMetadata) {
		t.Fatalf("Record(unsafe metadata) error = %v; want ErrUnsafeMetadata", err)
	}
	if err := service.Record(ctx, Event{
		Type:     EventLoginFailure,
		Outcome:  OutcomeFailure,
		Metadata: map[string]string{"client_supplied_label": "not whitelisted"},
	}); !errors.Is(err, ErrUnsafeMetadata) {
		t.Fatalf("Record(unlisted metadata) error = %v; want ErrUnsafeMetadata", err)
	}

	for _, eventType := range []string{EventLoginFailure, EventLoginSuccess, EventFolderCreated} {
		if err := service.Record(ctx, Event{Type: eventType, Outcome: OutcomeSuccess}); err != nil {
			t.Fatalf("Record(%s) error = %v", eventType, err)
		}
		now = now.Add(time.Second)
	}

	if _, err := service.List(ctx, "member", ListFilter{}); !errors.Is(err, ErrOwnerRoleNeeded) {
		t.Fatalf("member List() error = %v; want ErrOwnerRoleNeeded", err)
	}
	if _, err := service.List(ctx, "owner", ListFilter{Limit: MaximumPageSize + 1}); !errors.Is(err, ErrInvalidList) {
		t.Fatalf("oversized List() error = %v; want ErrInvalidList", err)
	}

	page, err := service.List(ctx, "owner", ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Events) != 2 || page.NextCursor == 0 {
		t.Fatalf("page = %+v; want two events and next cursor", page)
	}
	nextPage, err := service.List(ctx, "owner", ListFilter{Limit: 2, BeforeID: page.NextCursor})
	if err != nil {
		t.Fatalf("List(next) error = %v", err)
	}
	if len(nextPage.Events) != 1 {
		t.Fatalf("next page length = %d; want 1", len(nextPage.Events))
	}
	if err := service.Record(ctx, Event{
		Type: EventFileMoved, Outcome: OutcomeSuccess,
		ResourcePath: "docs/source.txt", DestinationPath: "archive/destination.txt",
	}); err != nil {
		t.Fatalf("Record(logical paths) error = %v", err)
	}
	pathPage, err := service.List(ctx, "owner", ListFilter{EventType: EventFileMoved, Limit: 1})
	if err != nil || len(pathPage.Events) != 1 || pathPage.Events[0].ResourcePath != "docs/source.txt" || pathPage.Events[0].DestinationPath != "archive/destination.txt" {
		t.Fatalf("logical path audit page = %+v, %v", pathPage, err)
	}
	for _, unsafePath := range []string{"/srv/personalcloud/files/secret", "../outside"} {
		if err := service.Record(ctx, Event{Type: EventFileDownloaded, Outcome: OutcomeSuccess, ResourcePath: unsafePath}); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("Record(unsafe path %q) error = %v; want ErrInvalidEvent", unsafePath, err)
		}
	}

	if _, err := db.Exec(`UPDATE audit_events SET outcome = 'denied' WHERE id = 1`); err == nil {
		t.Fatal("audit event update succeeded; want append-only trigger failure")
	}
	if _, err := db.Exec(`DELETE FROM audit_events WHERE id = 1`); err == nil {
		t.Fatal("audit event delete succeeded; want append-only trigger failure")
	}
}

func openAuditTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("database.Open() error = %v", err)
	}
	if err := database.Migrate(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("database.Migrate() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
