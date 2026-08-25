package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/config"
	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

func TestMaximumHTTPHeaderBytesIs64KiB(t *testing.T) {
	if maximumHTTPHeaderBytes != 64<<10 {
		t.Fatalf("maximumHTTPHeaderBytes = %d; want %d", maximumHTTPHeaderBytes, 64<<10)
	}
}

func TestRunFailsClosedWhenStorageVolumeIdentityIsMissing(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "state.db")
	storageRoot := filepath.Join(base, "storage")

	if err := os.Mkdir(storageRoot, 0o750); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := run(context.Background(), config.Server{
		DatabasePath:    databasePath,
		StorageRoot:     storageRoot,
		StorageVolumeID: "expected-volume",
	}, logger)
	if !errors.Is(err, storage.ErrStorageVolumeMismatch) {
		t.Fatalf(
			"run() error = %v; want ErrStorageVolumeMismatch",
			err,
		)
	}

	for _, directory := range []string{"files", "uploads", "trash"} {
		if _, err := os.Stat(filepath.Join(storageRoot, directory)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf(
				"%s exists after rejected server storage volume; want absent",
				directory,
			)
		}
	}
}

func TestRunRefusesDatabaseAlreadyOwnedByAnotherProcess(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "state.db")

	lock, err := database.AcquireProcessLock(databasePath)
	if err != nil {
		t.Fatalf("AcquireProcessLock() error = %v", err)
	}
	defer lock.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = run(context.Background(), config.Server{
		DatabasePath: databasePath,
	}, logger)
	if !errors.Is(err, database.ErrProcessLockBusy) {
		t.Fatalf("run() error = %v; want ErrProcessLockBusy", err)
	}
}

func TestStartupLoggingDoesNotExposePrivilegedPhysicalPaths(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	secretPath := "/var/lib/personalcloud/private/state.db"
	logStartupFailure(logger, errors.New("open "+secretPath+": permission denied"))
	if strings.Contains(output.String(), secretPath) || strings.Contains(output.String(), "permission denied") {
		t.Fatalf("startup log exposed wrapped error/path: %s", output.String())
	}
	if !strings.Contains(output.String(), `"error_type":"startup_failure"`) {
		t.Fatalf("startup log lacks safe error category: %s", output.String())
	}
}

func TestRunWaitsForCleanupWorkerWithinShutdownDeadline(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "state.db")
	storageRoot := filepath.Join(base, "storage")
	if err := os.Mkdir(storageRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	const volumeID = "shutdown-test-volume"
	if err := os.WriteFile(filepath.Join(storageRoot, ".swadrive-volume"), []byte(volumeID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{})
	logger := slog.New(&serverReadyHandler{ready: ready})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, config.Server{
			ListenAddress:         "127.0.0.1:0",
			DatabasePath:          databasePath,
			StorageRoot:           storageRoot,
			StorageVolumeID:       volumeID,
			UploadCleanupInterval: time.Hour,
		}, logger)
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not reach listening state")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("run(cancelled) error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("normal cleanup did not observe cancellation within the test deadline")
	}

	lock, err := database.AcquireProcessLock(databasePath)
	if err != nil {
		t.Fatalf("database lock not released after bounded cleanup shutdown wait: %v", err)
	}
	_ = lock.Close()
}

func TestRunFailsClosedOnInterruptedFileMutationIntent(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "state.db")
	storageRoot := filepath.Join(base, "storage")
	if err := os.Mkdir(storageRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	const volumeID = "unhealthy-index-volume"
	if err := os.WriteFile(filepath.Join(storageRoot, ".swadrive-volume"), []byte(volumeID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE file_index_state
		SET healthy = 0, unhealthy_reason = 'move-pending', updated_at = unixepoch()
		WHERE singleton = 1
	`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	err = run(context.Background(), config.Server{
		ListenAddress:         "127.0.0.1:0",
		DatabasePath:          databasePath,
		StorageRoot:           storageRoot,
		StorageVolumeID:       volumeID,
		UploadCleanupInterval: time.Hour,
	}, slog.New(slog.DiscardHandler))
	if !errors.Is(err, files.ErrIndexInconsistent) {
		t.Fatalf("run(unresolved mutation intent) error = %v; want ErrIndexInconsistent", err)
	}
}

type serverReadyHandler struct {
	ready chan struct{}
	once  sync.Once
}

func (handler *serverReadyHandler) Enabled(context.Context, slog.Level) bool { return true }

func (handler *serverReadyHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "SwaDrive server listening" {
		handler.once.Do(func() { close(handler.ready) })
	}
	return nil
}

func (handler *serverReadyHandler) WithAttrs([]slog.Attr) slog.Handler { return handler }
func (handler *serverReadyHandler) WithGroup(string) slog.Handler      { return handler }
