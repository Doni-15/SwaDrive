package uploads

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/database"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

func TestDifferentChunkIndexesWriteConcurrently(t *testing.T) {
	environment := newUploadTestEnvironment(t, 2)
	first := bytes.Repeat([]byte{0x11}, int(ChunkSize1MiB))
	second := bytes.Repeat([]byte{0x22}, int(ChunkSize1MiB))
	upload := environment.create(t, "parallel.bin", int64(len(first)+len(second)))
	release := make(chan struct{})
	started := make(chan struct{}, 2)

	results := make(chan error, 2)
	for index, content := range [][]byte{first, second} {
		go func(index int64, content []byte) {
			checksum := sha256.Sum256(content)
			_, err := environment.service.PutChunk(
				context.Background(), environment.identity, upload.ID, index,
				newBlockingReader(content, started, release), checksum[:],
			)
			results <- err
		}(int64(index), content)
	}
	waitForStarts(t, started, 2)
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("PutChunk() error = %v", err)
		}
	}
	completed, err := environment.service.Complete(context.Background(), environment.identity, upload.ID)
	if err != nil || completed.Status != StatusCompleted {
		t.Fatalf("Complete() = %s, %v; want completed", completed.Status, err)
	}
	contents, err := os.ReadFile(filepath.Join(environment.root, "files", "parallel.bin"))
	if err != nil || !bytes.Equal(contents, append(append([]byte(nil), first...), second...)) {
		t.Fatalf("parallel final content length = %d, error = %v", len(contents), err)
	}
}

func TestSameChunkIndexRaceIsIdempotentAndLockIsReclaimed(t *testing.T) {
	environment := newUploadTestEnvironment(t, 2)
	content := []byte("same chunk content")
	upload := environment.create(t, "same-index.bin", int64(len(content)))
	checksum := sha256.Sum256(content)
	release := make(chan struct{})
	close(release)
	started := make(chan struct{}, 2)
	results := make(chan struct {
		result PutResult
		err    error
	}, 2)
	for range 2 {
		go func() {
			result, err := environment.service.PutChunk(
				context.Background(), environment.identity, upload.ID, 0,
				newBlockingReader(content, started, release), checksum[:],
			)
			results <- struct {
				result PutResult
				err    error
			}{result: result, err: err}
		}()
	}
	idempotent := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("PutChunk(same index) error = %v", result.err)
		}
		if result.result.Idempotent {
			idempotent++
		}
	}
	if idempotent != 1 {
		t.Fatalf("idempotent responses = %d; want exactly 1", idempotent)
	}
	if len(environment.service.locks.entries) != 0 {
		t.Fatalf("upload lock entries = %d; want reclaimed", len(environment.service.locks.entries))
	}
}

func TestCompleteAndCancelWaitForInflightChunks(t *testing.T) {
	for _, operation := range []string{"complete", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			environment := newUploadTestEnvironment(t, 2)
			content := []byte("in-flight content")
			upload := environment.create(t, operation+".bin", int64(len(content)))
			checksum := sha256.Sum256(content)
			release := make(chan struct{})
			started := make(chan struct{}, 1)
			putDone := make(chan error, 1)
			go func() {
				_, err := environment.service.PutChunk(
					context.Background(), environment.identity, upload.ID, 0,
					newBlockingReader(content, started, release), checksum[:],
				)
				putDone <- err
			}()
			waitForStarts(t, started, 1)

			operationDone := make(chan error, 1)
			go func() {
				if operation == "complete" {
					_, err := environment.service.Complete(context.Background(), environment.identity, upload.ID)
					operationDone <- err
					return
				}
				operationDone <- environment.service.Cancel(context.Background(), environment.identity, upload.ID)
			}()
			select {
			case err := <-operationDone:
				t.Fatalf("%s returned before in-flight chunk finished: %v", operation, err)
			case <-time.After(30 * time.Millisecond):
			}
			close(release)
			if err := <-putDone; err != nil {
				t.Fatalf("PutChunk() error = %v", err)
			}
			if err := <-operationDone; err != nil {
				t.Fatalf("%s error = %v", operation, err)
			}
		})
	}
}

func TestExpirationCleanupWaitsForInflightChunk(t *testing.T) {
	environment := newUploadTestEnvironment(t, 2)
	content := []byte("cleanup race")
	upload := environment.create(t, "cleanup-race.bin", int64(len(content)))
	checksum := sha256.Sum256(content)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	putDone := make(chan error, 1)
	go func() {
		_, err := environment.service.PutChunk(
			context.Background(), environment.identity, upload.ID, 0,
			newBlockingReader(content, started, release), checksum[:],
		)
		putDone <- err
	}()
	waitForStarts(t, started, 1)
	now := time.Unix(1_800_000_000, 0)
	if _, err := environment.db.Exec(`UPDATE uploads SET created_at = ?, expires_at = ? WHERE id = ?`, now.Add(-2*time.Second).Unix(), now.Add(-time.Second).Unix(), upload.ID); err != nil {
		t.Fatalf("expire in-flight upload: %v", err)
	}
	cleanupDone := make(chan error, 1)
	go func() {
		_, err := environment.service.CleanupExpired(context.Background())
		cleanupDone <- err
	}()
	select {
	case err := <-cleanupDone:
		t.Fatalf("CleanupExpired() returned before chunk finished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-putDone; err != nil {
		t.Fatalf("PutChunk() error = %v", err)
	}
	if err := <-cleanupDone; err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}
	var status string
	if err := environment.db.QueryRow(`SELECT status FROM uploads WHERE id = ?`, upload.ID).Scan(&status); err != nil || status != string(StatusExpired) {
		t.Fatalf("cleanup race status = %q, %v; want expired", status, err)
	}
}

func TestChunkArithmeticHandlesInt64Boundaries(t *testing.T) {
	for _, totalSize := range []int64{0, 1, ChunkSize1MiB, ChunkSize1MiB + 1, math.MaxInt64} {
		chunks := totalChunks(totalSize, ChunkSize1MiB)
		if totalSize == 0 {
			if chunks != 0 {
				t.Fatalf("totalChunks(0) = %d; want 0", chunks)
			}
			continue
		}
		want := (totalSize-1)/ChunkSize1MiB + 1
		if chunks != want {
			t.Fatalf("totalChunks(%d) = %d; want %d", totalSize, chunks, want)
		}
		upload := Upload{TotalSize: totalSize, ChunkSize: ChunkSize1MiB, TotalChunks: chunks}
		offset, finalSize, err := chunkBounds(upload, chunks-1)
		if err != nil || offset < 0 || finalSize < 1 || finalSize > ChunkSize1MiB || offset > math.MaxInt64-finalSize {
			t.Fatalf("final chunk bounds for %d = %d, %d, %v", totalSize, offset, finalSize, err)
		}
		if _, _, err := chunkBounds(upload, chunks); !errors.Is(err, ErrInvalidUpload) {
			t.Fatalf("chunkBounds(out of range) error = %v; want ErrInvalidUpload", err)
		}
	}
	environment := newUploadTestEnvironment(t, 1)
	if _, err := environment.service.Create(context.Background(), environment.identity, CreateInput{
		TargetPath: "negative-overflow.bin", TotalSize: math.MinInt64, ChunkSize: -1,
	}); !errors.Is(err, ErrInvalidUpload) {
		t.Fatalf("Create(integer division overflow attempt) error = %v; want ErrInvalidUpload", err)
	}
	tooManyChunks := int64(MaximumChunksPerUpload)*ChunkSize1MiB + 1
	if _, err := environment.service.Create(context.Background(), environment.identity, CreateInput{
		TargetPath: "too-many-chunks.bin", TotalSize: tooManyChunks, ChunkSize: ChunkSize1MiB,
	}); !errors.Is(err, ErrInvalidUpload) {
		t.Fatalf("Create(excessive chunk count) error = %v; want ErrInvalidUpload", err)
	}
}

func TestGlobalChunkConcurrencyGateAndCancellation(t *testing.T) {
	environment := newUploadTestEnvironment(t, 1)
	content := []byte("bounded")
	checksum := sha256.Sum256(content)
	firstUpload := environment.create(t, "gate-first.bin", int64(len(content)))
	secondUpload := environment.create(t, "gate-second.bin", int64(len(content)))
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	results := make(chan error, 2)

	go func() {
		_, err := environment.service.PutChunk(context.Background(), environment.identity, firstUpload.ID, 0, newBlockingReader(content, started, release), checksum[:])
		results <- err
	}()
	waitForStarts(t, started, 1)

	cancelledContext, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := environment.service.PutChunk(cancelledContext, environment.identity, secondUpload.ID, 0, newBlockingReader(content, started, release), checksum[:])
		results <- err
	}()
	select {
	case <-started:
		t.Fatal("second chunk entered while the global gate was full")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	if err := <-results; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued PutChunk() error = %v; want context.Canceled", err)
	}
	close(release)
	if err := <-results; err != nil {
		t.Fatalf("first PutChunk() error = %v", err)
	}
}

func TestChunkAdmissionQueueIsBounded(t *testing.T) {
	environment := newUploadTestEnvironment(t, 1)
	content := []byte("bounded queue")
	upload := environment.create(t, "bounded-queue.bin", int64(len(content)))
	for range cap(environment.service.chunkAdmissions) {
		environment.service.chunkAdmissions <- struct{}{}
	}
	checksum := sha256.Sum256(content)
	if _, err := environment.service.PutChunk(context.Background(), environment.identity, upload.ID, 0, bytes.NewReader(content), checksum[:]); !errors.Is(err, ErrUploadBusy) {
		t.Fatalf("PutChunk(full admission queue) error = %v; want ErrUploadBusy", err)
	}
	for range cap(environment.service.chunkAdmissions) {
		<-environment.service.chunkAdmissions
	}
}

func TestCompletionAuditFailureLeavesRecoverableFinalizingState(t *testing.T) {
	environment := newUploadTestEnvironment(t, 2)
	content := []byte("recover finalization")
	upload := environment.create(t, "recovery.bin", int64(len(content)))

	checksum := sha256.Sum256(content)
	if _, err := environment.service.PutChunk(
		context.Background(),
		environment.identity,
		upload.ID,
		0,
		bytes.NewReader(content),
		checksum[:],
	); err != nil {
		t.Fatalf("PutChunk() error = %v", err)
	}

	var indexed int
	if err := environment.db.QueryRow(
		`SELECT COUNT(*) FROM file_entries WHERE logical_path = ? AND trash_entry_id IS NULL`,
		upload.TargetPath,
	).Scan(&indexed); err != nil || indexed != 0 {
		t.Fatalf("index before publication = %d, %v; want 0", indexed, err)
	}

	installUploadAuditFailureTrigger(t, environment.db)
	if _, err := environment.service.Complete(context.Background(), environment.identity, upload.ID); err == nil {
		t.Fatal("Complete() succeeded with forced audit failure")
	}

	var status string
	if err := environment.db.QueryRow(
		`SELECT status FROM uploads WHERE id = ?`,
		upload.ID,
	).Scan(&status); err != nil || status != string(StatusFinalizing) {
		t.Fatalf("interrupted completion status = %q, %v; want finalizing", status, err)
	}

	recoveryPath := filepath.Join(environment.root, "files", "recovery.bin")
	if contents, err := os.ReadFile(recoveryPath); err != nil || !bytes.Equal(contents, content) {
		t.Fatalf("recovery destination = %q, %v", contents, err)
	}

	physicalModifiedAt := time.Unix(1_700_000_123, 0).UTC()
	if err := os.Chtimes(recoveryPath, physicalModifiedAt, physicalModifiedAt); err != nil {
		t.Fatalf("set recovery destination mtime: %v", err)
	}

	dropUploadAuditFailureTrigger(t, environment.db)

	completed, err := environment.service.Complete(context.Background(), environment.identity, upload.ID)
	if err != nil || completed.Status != StatusCompleted {
		t.Fatalf("Complete(recovery) = %s, %v; want completed", completed.Status, err)
	}

	var indexedModifiedAt int64
	if err := environment.db.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(modified_at), 0)
		 FROM file_entries
		 WHERE logical_path = ? AND trash_entry_id IS NULL`,
		upload.TargetPath,
	).Scan(&indexed, &indexedModifiedAt); err != nil {
		t.Fatal(err)
	}
	if indexed != 1 {
		t.Fatalf("index after publication recovery = %d; want 1", indexed)
	}
	if indexedModifiedAt != physicalModifiedAt.Unix() {
		t.Fatalf(
			"recovered indexed mtime = %d; want physical mtime %d",
			indexedModifiedAt,
			physicalModifiedAt.Unix(),
		)
	}
}

func TestStartupFinalizingReconciliationStateMatrix(t *testing.T) {
	t.Run("part exists destination absent resets pending", func(t *testing.T) {
		environment := newUploadTestEnvironment(t, 1)
		upload := environment.create(t, "reset.bin", 0)

		if err := environment.service.repository.TransitionStatus(
			context.Background(),
			upload.UserID,
			upload.ID,
			StatusPending,
			StatusFinalizing,
			upload.UpdatedAt,
		); err != nil {
			t.Fatal(err)
		}

		count, err := environment.service.ReconcileFinalizing(context.Background())
		if err != nil || count != 1 {
			t.Fatalf("ReconcileFinalizing() = %d, %v", count, err)
		}

		reset, err := environment.service.repository.Find(context.Background(), upload.UserID, upload.ID)
		if err != nil || reset.Status != StatusPending {
			t.Fatalf("reset status = %s, %v; want pending", reset.Status, err)
		}
	})

	t.Run("published destination completes with physical metadata", func(t *testing.T) {
		environment := newUploadTestEnvironment(t, 1)
		content := []byte("legacy published content")
		upload := environment.create(t, "published.bin", int64(len(content)))
		checksum := sha256.Sum256(content)
		if _, err := environment.service.PutChunk(context.Background(), environment.identity, upload.ID, 0, bytes.NewReader(content), checksum[:]); err != nil {
			t.Fatalf("PutChunk(legacy fixture) error = %v", err)
		}

		if err := environment.service.repository.TransitionStatus(
			context.Background(),
			upload.UserID,
			upload.ID,
			StatusPending,
			StatusFinalizing,
			upload.UpdatedAt,
		); err != nil {
			t.Fatal(err)
		}

		destination, _ := storage.ParsePath(upload.TargetPath, false)
		if err := environment.manager.FinalizePart(upload.PartName, destination); err != nil {
			t.Fatal(err)
		}

		physicalModifiedAt := time.Unix(1_700_000_456, 0).UTC()
		destinationPath := filepath.Join(environment.root, "files", upload.TargetPath)
		if err := os.Chtimes(destinationPath, physicalModifiedAt, physicalModifiedAt); err != nil {
			t.Fatalf("set published destination mtime: %v", err)
		}

		count, err := environment.service.ReconcileFinalizing(context.Background())
		if err != nil || count != 1 {
			t.Fatalf("ReconcileFinalizing() = %d, %v", count, err)
		}

		completed, err := environment.service.repository.Find(context.Background(), upload.UserID, upload.ID)
		if err != nil || completed.Status != StatusCompleted {
			t.Fatalf("published status = %s, %v; want completed", completed.Status, err)
		}
		if !bytes.Equal(completed.WholeSHA256, checksum[:]) {
			t.Fatalf("published checksum = %x; want %x", completed.WholeSHA256, checksum)
		}

		var indexed int
		var indexedModifiedAt int64
		if err := environment.db.QueryRow(
			`SELECT COUNT(*), COALESCE(MAX(modified_at), 0)
			 FROM file_entries
			 WHERE logical_path = 'published.bin' AND trash_entry_id IS NULL`,
		).Scan(&indexed, &indexedModifiedAt); err != nil {
			t.Fatal(err)
		}
		if indexed != 1 {
			t.Fatalf("published index count = %d; want 1", indexed)
		}
		if indexedModifiedAt != physicalModifiedAt.Unix() {
			t.Fatalf(
				"published indexed mtime = %d; want physical mtime %d",
				indexedModifiedAt,
				physicalModifiedAt.Unix(),
			)
		}
	})

	t.Run("published destination with wrong size fails closed", func(t *testing.T) {
		environment := newUploadTestEnvironment(t, 1)
		upload := environment.create(t, "wrong-size.bin", 8)

		if err := environment.service.repository.TransitionStatus(
			context.Background(),
			upload.UserID,
			upload.ID,
			StatusPending,
			StatusFinalizing,
			upload.UpdatedAt,
		); err != nil {
			t.Fatal(err)
		}

		if err := environment.manager.RemovePart(upload.PartName); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(environment.root, "files", upload.TargetPath),
			[]byte("x"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}

		if _, err := environment.service.ReconcileFinalizing(context.Background()); !errors.Is(err, ErrFinalizationReconciliation) {
			t.Fatalf(
				"ReconcileFinalizing() error = %v; want ErrFinalizationReconciliation",
				err,
			)
		}

		var healthy int
		if err := environment.db.QueryRow(
			`SELECT healthy FROM file_index_state WHERE singleton = 1`,
		).Scan(&healthy); err != nil {
			t.Fatal(err)
		}
		if healthy != 0 {
			t.Fatalf("file index healthy = %d; want 0 after published-size mismatch", healthy)
		}
	})

	for _, test := range []struct {
		name        string
		partExists  bool
		destination bool
	}{
		{name: "both exist is ambiguous", partExists: true, destination: true},
		{name: "neither exists is data loss", partExists: false, destination: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := newUploadTestEnvironment(t, 1)
			upload := environment.create(t, "ambiguous.bin", 0)

			if err := environment.service.repository.TransitionStatus(
				context.Background(),
				upload.UserID,
				upload.ID,
				StatusPending,
				StatusFinalizing,
				upload.UpdatedAt,
			); err != nil {
				t.Fatal(err)
			}

			if !test.partExists {
				if err := environment.manager.RemovePart(upload.PartName); err != nil {
					t.Fatal(err)
				}
			}
			if test.destination {
				if err := os.WriteFile(
					filepath.Join(environment.root, "files", upload.TargetPath),
					nil,
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			}

			if _, err := environment.service.ReconcileFinalizing(context.Background()); !errors.Is(err, ErrFinalizationReconciliation) {
				t.Fatalf(
					"ReconcileFinalizing() error = %v; want ErrFinalizationReconciliation",
					err,
				)
			}
		})
	}
}

func TestUploadStatusDoesNotTouchStorageAndCancelledUploadIsNeverIndexed(t *testing.T) {
	environment := newUploadTestEnvironment(t, 1)
	upload := environment.create(t, "cancelled-index.bin", 0)
	if err := environment.service.Cancel(context.Background(), environment.identity, upload.ID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	var indexed int
	if err := environment.db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE logical_path = ?`, upload.TargetPath).Scan(&indexed); err != nil || indexed != 0 {
		t.Fatalf("cancelled upload index rows = %d, %v; want 0", indexed, err)
	}
	expired := environment.create(t, "expired-index.bin", 0)
	*environment.now = environment.now.Add(UploadLifetime + time.Second)
	if cleaned, err := environment.service.CleanupExpired(context.Background()); err != nil || cleaned != 1 {
		t.Fatalf("CleanupExpired() = %d, %v; want 1", cleaned, err)
	}
	if err := environment.db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE logical_path = ?`, expired.TargetPath).Scan(&indexed); err != nil || indexed != 0 {
		t.Fatalf("expired upload index rows = %d, %v; want 0", indexed, err)
	}
	pending := environment.create(t, "status-only.bin", 0)
	guard := &failOnUploadStorage{t: t}
	environment.service.storage = guard
	if _, err := environment.service.Get(context.Background(), environment.identity, pending.ID); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if guard.calls != 0 {
		t.Fatalf("upload status touched storage %d times", guard.calls)
	}
}

func TestCompleteAndCancelRepairAfterRequestCancellation(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		environment := newUploadTestEnvironment(t, 1)
		upload := environment.create(t, "cancelled-complete.bin", 0)
		ctx, cancel := context.WithCancel(context.Background())
		environment.service.storage = &cancelAfterUploadMutationStorage{Storage: environment.manager, cancelFinalize: cancel}

		completed, err := environment.service.Complete(ctx, environment.identity, upload.ID)
		if err != nil || completed.Status != StatusCompleted {
			t.Fatalf("Complete() = %s, %v; want completed", completed.Status, err)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("complete request context error = %v; want context.Canceled", ctx.Err())
		}
		emptyChecksum := sha256.Sum256(nil)
		if !bytes.Equal(completed.WholeSHA256, emptyChecksum[:]) {
			t.Fatalf("completed checksum = %x; want %x", completed.WholeSHA256, emptyChecksum)
		}
		if pending, err := environment.service.HasPendingFinalization(context.Background()); err != nil || pending {
			t.Fatalf("pending finalization = %t, %v; want false", pending, err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		environment := newUploadTestEnvironment(t, 1)
		upload := environment.create(t, "cancelled-cancel.bin", 0)
		ctx, cancel := context.WithCancel(context.Background())
		environment.service.storage = &cancelAfterUploadMutationStorage{Storage: environment.manager, cancelRemove: cancel}

		if err := environment.service.Cancel(ctx, environment.identity, upload.ID); err != nil {
			t.Fatalf("Cancel() error = %v", err)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("cancel request context error = %v; want context.Canceled", ctx.Err())
		}
		cancelled, err := environment.service.Get(context.Background(), environment.identity, upload.ID)
		if err != nil || cancelled.Status != StatusCancelled {
			t.Fatalf("cancelled upload = %s, %v; want cancelled", cancelled.Status, err)
		}
	})
}

func TestExpiredUploadCompletionRepairUsesIndependentFailClosedMarker(t *testing.T) {
	environment := newUploadTestEnvironment(t, 1)
	upload := environment.create(t, "expired-completion-repair.bin", 0)
	repository := &expiringCompletionRepository{Repository: environment.service.repository}
	environment.service.repository = repository
	environment.service.repairTimeout = 100 * time.Millisecond

	if _, err := environment.service.Complete(context.Background(), environment.identity, upload.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Complete() error = %v; want context.DeadlineExceeded", err)
	}
	if !errors.Is(repository.completionContextErr, context.DeadlineExceeded) {
		t.Fatalf("completion context error = %v; want context.DeadlineExceeded", repository.completionContextErr)
	}
	if repository.markerContextErr != nil {
		t.Fatalf("fail-closed marker inherited expired context: %v", repository.markerContextErr)
	}
	if err := files.NewSQLiteFileIndexRepository(environment.db).CheckHealthy(context.Background()); !errors.Is(err, files.ErrIndexInconsistent) {
		t.Fatalf("index health = %v; want files.ErrIndexInconsistent", err)
	}
	stored, err := repository.Repository.Find(context.Background(), upload.UserID, upload.ID)
	if err != nil || stored.Status != StatusFinalizing {
		t.Fatalf("durable upload state = %s, %v; want finalizing", stored.Status, err)
	}
	if _, err := os.Stat(filepath.Join(environment.root, "files", upload.TargetPath)); err != nil {
		t.Fatalf("published filesystem side effect missing: %v", err)
	}
}

func TestCompletionRehashesStoredChunksWithoutClientWholeChecksum(t *testing.T) {
	environment := newUploadTestEnvironment(t, 1)
	content := []byte("original bytes")
	upload := environment.create(t, "tampered.bin", int64(len(content)))
	checksum := sha256.Sum256(content)
	if _, err := environment.service.PutChunk(context.Background(), environment.identity, upload.ID, 0, bytes.NewReader(content), checksum[:]); err != nil {
		t.Fatalf("PutChunk() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(environment.root, "uploads", upload.PartName), []byte("tampered bytes"), 0o600); err != nil {
		t.Fatalf("tamper stored part: %v", err)
	}
	if _, err := environment.service.Complete(context.Background(), environment.identity, upload.ID); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Complete(tampered part) error = %v; want ErrChecksumMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(environment.root, "files", upload.TargetPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tampered destination exists: %v", err)
	}
}

func TestRestartCleanupRepairsExpiredPendingUploadWithMissingPart(t *testing.T) {
	environment := newUploadTestEnvironment(t, 1)
	upload := environment.create(t, "missing-part-after-restart.bin", 0)
	if err := environment.manager.RemovePart(upload.PartName); err != nil {
		t.Fatalf("RemovePart(crash fixture) error = %v", err)
	}
	*environment.now = environment.now.Add(UploadLifetime + time.Second)

	// Constructing a fresh service models restart: cleanup must treat an already
	// missing internal part as removed, then durably expire the known DB row.
	auditService := audit.NewService(audit.NewSQLiteRepository(environment.db), func() time.Time { return *environment.now })
	restarted := NewService(
		NewSQLiteRepository(environment.db),
		environment.manager,
		storage.NewMutationCoordinator(),
		auditService,
		0,
		1,
		func() time.Time { return *environment.now },
	)
	cleaned, err := restarted.CleanupExpired(context.Background())
	if err != nil || cleaned != 1 {
		t.Fatalf("CleanupExpired(restarted missing part) = %d, %v; want 1", cleaned, err)
	}
	stored, err := restarted.Get(context.Background(), environment.identity, upload.ID)
	if err != nil || stored.Status != StatusExpired {
		t.Fatalf("Get(repaired upload) = %s, %v; want expired", stored.Status, err)
	}
	var indexed int
	if err := environment.db.QueryRow(`SELECT COUNT(*) FROM file_entries WHERE logical_path = ?`, upload.TargetPath).Scan(&indexed); err != nil || indexed != 0 {
		t.Fatalf("missing-part expired upload index rows = %d, %v; want 0", indexed, err)
	}
}

type uploadTestEnvironment struct {
	service  *Service
	identity auth.Identity
	db       *sql.DB
	root     string
	manager  *storage.Manager
	now      *time.Time
}

func newUploadTestEnvironment(t *testing.T, concurrentChunks int) uploadTestEnvironment {
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
	service := NewService(NewSQLiteRepository(db), manager, storage.NewMutationCoordinator(), auditService, 0, concurrentChunks, func() time.Time { return now })
	t.Cleanup(func() {
		_ = manager.Close()
		_ = db.Close()
	})
	return uploadTestEnvironment{service: service, identity: identity, db: db, root: root, manager: manager, now: &now}
}

type failOnUploadStorage struct {
	t     *testing.T
	calls int
}

type cancelAfterUploadMutationStorage struct {
	Storage
	cancelFinalize context.CancelFunc
	cancelRemove   context.CancelFunc
}

type expiringCompletionRepository struct {
	Repository
	completionContextErr error
	markerContextErr     error
}

func (repository *expiringCompletionRepository) CompleteWithAudit(ctx context.Context, _ int64, _ string, _ time.Time, _ files.Entry, _ audit.Event) error {
	<-ctx.Done()
	repository.completionContextErr = ctx.Err()
	return ctx.Err()
}

func (repository *expiringCompletionRepository) MarkIndexUnhealthy(ctx context.Context, reason string, updatedAt time.Time) error {
	repository.markerContextErr = ctx.Err()
	return repository.Repository.MarkIndexUnhealthy(ctx, reason, updatedAt)
}

func (storageManager *cancelAfterUploadMutationStorage) FinalizePart(partName string, destination storage.Path) error {
	err := storageManager.Storage.FinalizePart(partName, destination)
	if err == nil && storageManager.cancelFinalize != nil {
		storageManager.cancelFinalize()
		storageManager.cancelFinalize = nil
	}
	return err
}

func (storageManager *cancelAfterUploadMutationStorage) RemovePart(partName string) error {
	err := storageManager.Storage.RemovePart(partName)
	if err == nil && storageManager.cancelRemove != nil {
		storageManager.cancelRemove()
		storageManager.cancelRemove = nil
	}
	return err
}

func (guard *failOnUploadStorage) called() error {
	guard.calls++
	guard.t.Error("upload status called physical storage")
	return errors.New("unexpected storage call")
}

func (guard *failOnUploadStorage) PrepareUpload(storage.Path) error  { return guard.called() }
func (guard *failOnUploadStorage) CreatePart(string) error           { return guard.called() }
func (guard *failOnUploadStorage) OpenPart(string) (*os.File, error) { return nil, guard.called() }
func (guard *failOnUploadStorage) OpenDownload(storage.Path) (*os.File, storage.Entry, error) {
	return nil, storage.Entry{}, guard.called()
}
func (guard *failOnUploadStorage) RemovePart(string) error                 { return guard.called() }
func (guard *failOnUploadStorage) PartInfo(string) (os.FileInfo, error)    { return nil, guard.called() }
func (guard *failOnUploadStorage) FinalizePart(string, storage.Path) error { return guard.called() }
func (guard *failOnUploadStorage) FinalizationState(string, storage.Path) (storage.PublicationState, error) {
	return storage.PublicationState{}, guard.called()
}
func (guard *failOnUploadStorage) CheckAvailable(uint64, uint64) error { return guard.called() }

func (environment uploadTestEnvironment) create(t *testing.T, target string, totalSize int64) Upload {
	t.Helper()
	upload, err := environment.service.Create(context.Background(), environment.identity, CreateInput{
		TargetPath: target, TotalSize: totalSize, ChunkSize: ChunkSize1MiB,
	})
	if err != nil {
		t.Fatalf("Create(%s) error = %v", target, err)
	}
	return upload
}

type blockingReader struct {
	once    sync.Once
	reader  io.Reader
	started chan<- struct{}
	release <-chan struct{}
}

func newBlockingReader(content []byte, started chan<- struct{}, release <-chan struct{}) *blockingReader {
	return &blockingReader{reader: bytes.NewReader(content), started: started, release: release}
}

func (reader *blockingReader) Read(buffer []byte) (int, error) {
	reader.once.Do(func() {
		reader.started <- struct{}{}
		<-reader.release
	})
	return reader.reader.Read(buffer)
}

func waitForStarts(t *testing.T, started <-chan struct{}, count int) {
	t.Helper()
	for range count {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("only part of the expected concurrent work started")
		}
	}
}

func installUploadAuditFailureTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		CREATE TRIGGER force_upload_audit_failure
		BEFORE INSERT ON audit_events
		BEGIN
			SELECT RAISE(ABORT, 'forced audit failure');
		END
	`); err != nil {
		t.Fatalf("create forced audit failure trigger: %v", err)
	}
}

func dropUploadAuditFailureTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP TRIGGER force_upload_audit_failure`); err != nil {
		t.Fatalf("drop forced audit failure trigger: %v", err)
	}
}
