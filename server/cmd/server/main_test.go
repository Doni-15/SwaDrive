package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/config"
	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
	"github.com/Doni-15/SwaDrive/server/internal/uploads"
)

func TestMaximumHTTPHeaderBytesIs64KiB(t *testing.T) {
	if maximumHTTPHeaderBytes != 64<<10 {
		t.Fatalf("maximumHTTPHeaderBytes = %d; want %d", maximumHTTPHeaderBytes, 64<<10)
	}
}

func TestHTTPServerTimeoutPolicyIsBounded(t *testing.T) {
	if serverReadTimeout != 30*time.Second || serverWriteTimeout != 30*time.Second {
		t.Fatalf("server timeouts = %v/%v; want 30s/30s", serverReadTimeout, serverWriteTimeout)
	}
}

func TestRunStartsHTTPServiceWhenStorageVolumeIdentityIsMissing(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "state.db")
	storageRoot := filepath.Join(base, "storage")

	if err := os.Mkdir(storageRoot, 0o750); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{})
	logger := slog.New(&serverReadyHandler{ready: ready})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, config.Server{
			ListenAddress:         "127.0.0.1:0",
			DatabasePath:          databasePath,
			StorageRoot:           storageRoot,
			StorageVolumeID:       "expected-volume",
			UploadCleanupInterval: time.Hour,
		}, logger)
	}()
	select {
	case <-ready:
	case err := <-result:
		t.Fatalf("run() stopped before listening: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("degraded server did not reach listening state")
	}
	select {
	case err := <-result:
		t.Fatalf("degraded server stopped unexpectedly: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("run(cancelled) error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("degraded server did not stop after cancellation")
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

func TestDegradedStartupClosesMetadataGateForPendingFilesystemReconciliation(t *testing.T) {
	for _, pendingState := range []string{"trash", "upload"} {
		t.Run(pendingState, func(t *testing.T) {
			base := t.TempDir()
			databasePath := filepath.Join(base, "state.db")
			storageRoot := filepath.Join(base, "storage")
			if err := os.Mkdir(storageRoot, 0o750); err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(context.Background(), databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := database.Migrate(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			result, err := db.Exec(`
				INSERT INTO users (username, password_hash, role, created_at, updated_at)
				VALUES ('owner', 'test-only-hash', 'owner', 1, 1)
			`)
			if err != nil {
				t.Fatal(err)
			}
			userID, _ := result.LastInsertId()
			switch pendingState {
			case "trash":
				_, err = db.Exec(`
					INSERT INTO trash_entries (id, user_id, original_path, trash_name, trashed_at, state, updated_at)
					VALUES ('1111111111111111', ?, 'pending.txt', '1111111111111111', 1, 'trashing', 1)
				`, userID)
			case "upload":
				_, err = db.Exec(`
					INSERT INTO uploads (
						id, user_id, target_path, part_name, total_size, chunk_size, total_chunks,
						status, created_at, updated_at, expires_at
					) VALUES ('2222222222222222', ?, 'pending.bin', '2222222222222222.part', 0, ?, 0, 'finalizing', 1, 1, 2)
				`, userID, uploads.ChunkSize1MiB)
			}
			if err != nil {
				t.Fatal(err)
			}
			reason := "trash:1111111111111111"
			if pendingState == "upload" {
				reason = "upload:2222222222222222"
			}
			if _, err := db.Exec(`
				UPDATE file_index_state
				SET healthy = 0, unhealthy_reason = ?, updated_at = 1
				WHERE singleton = 1
			`, reason); err != nil {
				t.Fatal(err)
			}

			provider := storage.OpenProvider(storageRoot, "missing-volume-marker", nil)
			auditService := audit.NewService(audit.NewSQLiteRepository(db), nil)
			coordinator := storage.NewMutationCoordinator()
			index := files.NewSQLiteFileIndexRepository(db)
			filesService := files.NewService(provider, files.NewSQLiteTrashRepository(db), index, coordinator, auditService, files.DefaultConcurrentDownloads, nil)
			uploadService := uploads.NewService(uploads.NewSQLiteRepository(db), provider, coordinator, auditService, 0, uploads.DefaultConcurrentChunks, nil)

			metadataAvailable, err := reconcileAvailableStorage(context.Background(), provider, filesService, uploadService, index)
			if err != nil || metadataAvailable {
				t.Fatalf("reconcileAvailableStorage() = %t, %v; want false, nil", metadataAvailable, err)
			}
		})
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
	defer cancel()
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

func TestRunRemainsAliveAndHealthDegradesAfterRuntimeStorageLoss(t *testing.T) {
	base := t.TempDir()
	databasePath := filepath.Join(base, "state.db")
	storageRoot := filepath.Join(base, "storage")
	if err := os.Mkdir(storageRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	const volumeID = "runtime-server-volume"
	if err := os.WriteFile(filepath.Join(storageRoot, ".swadrive-volume"), []byte(volumeID+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenAddress := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ready := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- run(ctx, config.Server{
			ListenAddress:         listenAddress,
			DatabasePath:          databasePath,
			StorageRoot:           storageRoot,
			StorageVolumeID:       volumeID,
			UploadCleanupInterval: time.Hour,
		}, slog.New(&serverReadyHandler{ready: ready}))
	}()
	select {
	case <-ready:
	case err := <-result:
		t.Fatalf("run() stopped before listening: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not reach listening state")
	}

	client := &http.Client{Timeout: time.Second}
	healthURL := "http://" + listenAddress + "/api/v1/health"
	waitForHealthBody := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			response, requestErr := client.Get(healthURL)
			if requestErr == nil {
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr == nil && response.StatusCode == http.StatusOK && string(body) == want {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("health did not become %s", want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitForHealthBody(`{"status":"ok","storage":"available"}`)

	detachedRoot := filepath.Join(base, "detached-storage")
	if err := os.Rename(storageRoot, detachedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(storageRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	waitForHealthBody(`{"status":"degraded","storage":"unavailable"}`)
	select {
	case err := <-result:
		t.Fatalf("server stopped after runtime storage loss: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	entries, err := os.ReadDir(storageRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("fallback root contains %d entries after health probe; want empty", len(entries))
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("run(cancelled) error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
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
