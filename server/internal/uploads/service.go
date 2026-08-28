package uploads

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/files"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

const (
	cleanupBatchSize             = 100
	finalizingReconcileBatchSize = 100
	maximumFinalizingReconcile   = 1000
	consistencyRepairTimeout     = 10 * time.Second
)

type Storage interface {
	PrepareUpload(destination storage.Path) error
	CreatePart(name string) error
	OpenPart(name string) (*os.File, error)
	RemovePart(name string) error
	PartInfo(name string) (os.FileInfo, error)
	FinalizePart(partName string, destination storage.Path) error
	FinalizationState(partName string, destination storage.Path) (storage.PublicationState, error)
	CheckAvailable(required, reserve uint64) error
}

type AuditRecorder interface {
	Record(ctx context.Context, event audit.Event) error
}

type Service struct {
	repository      Repository
	storage         Storage
	audit           AuditRecorder
	reserve         uint64
	now             func() time.Time
	locks           *uploadLocks
	mutations       *storage.MutationCoordinator
	chunkSlots      chan struct{}
	chunkAdmissions chan struct{}
}

func NewService(repository Repository, storageManager Storage, mutationCoordinator *storage.MutationCoordinator, auditRecorder AuditRecorder, reserve uint64, maxConcurrentChunks int, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if maxConcurrentChunks < 1 || maxConcurrentChunks > MaximumConcurrentChunks {
		maxConcurrentChunks = DefaultConcurrentChunks
	}
	if mutationCoordinator == nil {
		mutationCoordinator = storage.NewMutationCoordinator()
	}
	return &Service{
		repository:      repository,
		storage:         storageManager,
		audit:           auditRecorder,
		reserve:         reserve,
		now:             now,
		locks:           newUploadLocks(),
		mutations:       mutationCoordinator,
		chunkSlots:      make(chan struct{}, maxConcurrentChunks),
		chunkAdmissions: make(chan struct{}, maxConcurrentChunks*4),
	}
}

func (service *Service) Create(ctx context.Context, identity auth.Identity, input CreateInput) (Upload, error) {
	if identity.User.Role != auth.RoleOwner {
		return Upload{}, files.ErrOwnerRequired
	}
	destination, err := storage.ParsePath(input.TargetPath, false)
	if err != nil {
		return Upload{}, err
	}
	if input.ChunkSize == 0 {
		input.ChunkSize = DefaultChunkSize
	}
	if input.TotalSize < 0 || !allowedChunkSize(input.ChunkSize) || (len(input.WholeSHA256) != 0 && len(input.WholeSHA256) != sha256.Size) {
		return Upload{}, ErrInvalidUpload
	}
	totalChunkCount := totalChunks(input.TotalSize, input.ChunkSize)
	if totalChunkCount > MaximumChunksPerUpload {
		return Upload{}, ErrInvalidUpload
	}
	if err := service.storage.CheckAvailable(uint64(input.TotalSize), service.reserve); err != nil {
		return Upload{}, err
	}
	if err := service.storage.PrepareUpload(destination); err != nil {
		return Upload{}, err
	}

	id, err := newUploadID()
	if err != nil {
		return Upload{}, err
	}
	partName := id + ".part"
	if err := service.storage.CreatePart(partName); err != nil {
		return Upload{}, err
	}

	now := service.now().UTC()
	upload := Upload{
		ID:          id,
		UserID:      identity.User.ID,
		TargetPath:  destination.String(),
		PartName:    partName,
		TotalSize:   input.TotalSize,
		ChunkSize:   input.ChunkSize,
		TotalChunks: totalChunkCount,
		WholeSHA256: append([]byte(nil), input.WholeSHA256...),
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpiresAt:   now.Add(UploadLifetime),
	}
	if err := service.repository.CreateWithAudit(ctx, upload, service.event(identity, upload.ID, upload.TargetPath, audit.EventUploadCreated, audit.OutcomeSuccess, nil)); err != nil {
		removeErr := service.storage.RemovePart(partName)
		return Upload{}, errors.Join(err, removeErr)
	}
	return upload, nil
}

func (service *Service) Get(ctx context.Context, identity auth.Identity, id string) (Upload, error) {
	if identity.User.Role != auth.RoleOwner {
		return Upload{}, files.ErrOwnerRequired
	}
	if !validUploadID(id) {
		return Upload{}, ErrUploadNotFound
	}
	return service.repository.Find(ctx, identity.User.ID, id)
}

func (service *Service) ExpectedChunk(ctx context.Context, identity auth.Identity, id string, index int64) (int64, error) {
	upload, err := service.Get(ctx, identity, id)
	if err != nil {
		return 0, err
	}
	if upload.Status != StatusPending || !upload.ExpiresAt.After(service.now().UTC()) {
		return 0, ErrUploadState
	}
	_, expected, err := chunkBounds(upload, index)
	return expected, err
}

func (service *Service) PutChunk(
	ctx context.Context,
	identity auth.Identity,
	id string,
	index int64,
	body io.Reader,
	clientChecksum []byte,
) (PutResult, error) {
	if identity.User.Role != auth.RoleOwner {
		return PutResult{}, files.ErrOwnerRequired
	}
	if !validUploadID(id) || len(clientChecksum) != sha256.Size {
		return PutResult{}, ErrInvalidUpload
	}
	select {
	case service.chunkAdmissions <- struct{}{}:
		defer func() { <-service.chunkAdmissions }()
	default:
		return PutResult{}, ErrUploadBusy
	}
	select {
	case service.chunkSlots <- struct{}{}:
		defer func() { <-service.chunkSlots }()
	case <-ctx.Done():
		return PutResult{}, ctx.Err()
	}

	unlock := service.locks.lockChunk(id, index)
	defer unlock()

	upload, err := service.repository.Find(ctx, identity.User.ID, id)
	if err != nil {
		return PutResult{}, err
	}
	if upload.Status != StatusPending || !upload.ExpiresAt.After(service.now().UTC()) {
		return PutResult{}, ErrUploadState
	}
	offset, expectedSize, err := chunkBounds(upload, index)
	if err != nil {
		return PutResult{}, err
	}

	existing, err := service.repository.FindChunk(ctx, upload.ID, index)
	chunkAlreadyRecorded := err == nil
	if err != nil && !errors.Is(err, ErrChunkNotFound) {
		return PutResult{}, err
	}

	var partFile *os.File
	writer := io.Writer(io.Discard)
	if !chunkAlreadyRecorded {
		if err := service.storage.CheckAvailable(uint64(expectedSize), service.reserve); err != nil {
			return PutResult{}, err
		}
		partFile, err = service.storage.OpenPart(upload.PartName)
		if err != nil {
			return PutResult{}, err
		}
		writer = io.NewOffsetWriter(partFile, offset)
	}

	hasher := sha256.New()
	copyBuffer := make([]byte, 64*1024)
	bytesRead, readErr := io.CopyBuffer(
		io.MultiWriter(writer, hasher),
		io.LimitReader(&contextReader{ctx: ctx, reader: body}, expectedSize+1),
		copyBuffer,
	)
	if readErr != nil {
		if partFile != nil {
			_ = partFile.Close()
		}
		return PutResult{}, fmt.Errorf("stream upload chunk: %w", readErr)
	}
	if bytesRead != expectedSize {
		if partFile != nil {
			_ = partFile.Close()
		}
		return PutResult{}, errors.Join(ErrChunkLength, service.recordSecurityFailure(ctx, identity, upload.ID, "chunk_length"))
	}
	computedChecksum := hasher.Sum(nil)
	if subtle.ConstantTimeCompare(computedChecksum, clientChecksum) != 1 {
		if partFile != nil {
			_ = partFile.Close()
		}
		return PutResult{}, errors.Join(ErrChecksumMismatch, service.recordSecurityFailure(ctx, identity, upload.ID, "chunk_checksum"))
	}

	if chunkAlreadyRecorded {
		if existing.Size == expectedSize && existing.Offset == offset && subtle.ConstantTimeCompare(existing.SHA256, computedChecksum) == 1 {
			upload, findErr := service.repository.Find(ctx, identity.User.ID, id)
			return PutResult{Upload: upload, Idempotent: true}, findErr
		}
		return PutResult{}, errors.Join(ErrChunkConflict, service.recordSecurityFailure(ctx, identity, upload.ID, "chunk_retry_conflict"))
	}
	if err := partFile.Sync(); err != nil {
		_ = partFile.Close()
		return PutResult{}, fmt.Errorf("sync upload chunk: %w", err)
	}
	if err := partFile.Close(); err != nil {
		return PutResult{}, fmt.Errorf("close upload chunk: %w", err)
	}

	now := service.now().UTC()
	if err := service.repository.RecordChunk(ctx, Chunk{
		UploadID:   upload.ID,
		Index:      index,
		Offset:     offset,
		Size:       expectedSize,
		SHA256:     computedChecksum,
		ReceivedAt: now,
	}, now); err != nil {
		return PutResult{}, err
	}
	upload, err = service.repository.Find(ctx, identity.User.ID, id)
	return PutResult{Upload: upload}, err
}

func (service *Service) Complete(ctx context.Context, identity auth.Identity, id string) (Upload, error) {
	if identity.User.Role != auth.RoleOwner {
		return Upload{}, files.ErrOwnerRequired
	}
	if !validUploadID(id) {
		return Upload{}, ErrUploadNotFound
	}
	unlock := service.locks.lockExclusive(id)
	defer unlock()
	unlockMutation := service.mutations.Lock()
	defer unlockMutation()

	upload, err := service.repository.Find(ctx, identity.User.ID, id)
	if err != nil {
		return Upload{}, err
	}
	if upload.Status == StatusCompleted {
		return upload, nil
	}
	if upload.Status != StatusPending && upload.Status != StatusFinalizing {
		return Upload{}, ErrUploadState
	}
	destination, err := storage.ParsePath(upload.TargetPath, false)
	if err != nil {
		return Upload{}, err
	}

	if upload.Status == StatusFinalizing {
		state, err := service.storage.FinalizationState(upload.PartName, destination)
		if err != nil {
			return Upload{}, err
		}
		if !state.PartExists && state.DestinationExists {
			now := service.now().UTC()
			var completed Upload
			err := service.runConsistencyRepair(ctx, func(repairContext context.Context) error {
				if err := service.validatePublishedState(repairContext, upload, state, now); err != nil {
					return err
				}
				if err := service.completePublished(repairContext, upload, identity, state.DestinationModifiedAt, now, nil); err != nil {
					return err
				}
				var findErr error
				completed, findErr = service.repository.Find(repairContext, upload.UserID, upload.ID)
				return findErr
			})
			return completed, err
		}
		if !state.PartExists || state.DestinationExists {
			return Upload{}, ErrUploadState
		}
	}

	if err := service.repository.ValidateChunks(ctx, upload, nil); err != nil {
		return Upload{}, err
	}
	partInfo, err := service.storage.PartInfo(upload.PartName)
	if err != nil {
		return Upload{}, err
	}
	if partInfo.Size() != upload.TotalSize {
		return Upload{}, errors.Join(ErrChunkLength, service.recordSecurityFailure(ctx, identity, upload.ID, "part_size"))
	}
	wholeSHA256, err := service.verifyAndSyncPart(ctx, upload)
	if err != nil {
		return Upload{}, errors.Join(err, service.recordSecurityFailure(ctx, identity, upload.ID, "whole_file_integrity"))
	}
	upload.WholeSHA256 = wholeSHA256

	now := service.now().UTC()
	transitioned := upload.Status == StatusPending
	if err := service.repository.PrepareFinalization(ctx, upload.UserID, upload.ID, upload.Status, wholeSHA256, now); err != nil {
		return Upload{}, err
	}
	if err := service.storage.FinalizePart(upload.PartName, destination); err != nil {
		var rollbackErr error
		if transitioned {
			rollbackErr = service.runConsistencyRepair(ctx, func(repairContext context.Context) error {
				return service.repository.ResetFinalizing(repairContext, upload.UserID, upload.ID, now)
			})
		}
		return Upload{}, errors.Join(err, rollbackErr)
	}
	var completed Upload
	err = service.runConsistencyRepair(ctx, func(repairContext context.Context) error {
		if err := service.completePublished(repairContext, upload, identity, partInfo.ModTime().UTC(), now, nil); err != nil {
			return err
		}
		var findErr error
		completed, findErr = service.repository.Find(repairContext, upload.UserID, upload.ID)
		return findErr
	})
	return completed, err
}

func (service *Service) Cancel(ctx context.Context, identity auth.Identity, id string) error {
	if identity.User.Role != auth.RoleOwner {
		return files.ErrOwnerRequired
	}
	if !validUploadID(id) {
		return ErrUploadNotFound
	}
	unlock := service.locks.lockExclusive(id)
	defer unlock()

	upload, err := service.repository.Find(ctx, identity.User.ID, id)
	if err != nil {
		return err
	}
	if upload.Status != StatusPending {
		return ErrUploadState
	}
	if err := service.storage.RemovePart(upload.PartName); err != nil {
		return err
	}
	now := service.now().UTC()
	return service.runConsistencyRepair(ctx, func(repairContext context.Context) error {
		return service.repository.CancelWithAudit(repairContext, upload.UserID, upload.ID, now, service.event(identity, upload.ID, upload.TargetPath, audit.EventUploadCancelled, audit.OutcomeSuccess, nil))
	})
}

func (service *Service) CleanupExpired(ctx context.Context) (int, error) {
	now := service.now().UTC()
	expired, err := service.repository.ListExpired(ctx, now, cleanupBatchSize)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, candidate := range expired {
		if err := ctx.Err(); err != nil {
			return cleaned, err
		}
		unlock := service.locks.lockExclusive(candidate.ID)
		upload, findErr := service.repository.Find(ctx, candidate.UserID, candidate.ID)
		didClean := false
		if findErr == nil && upload.Status == StatusPending && !upload.ExpiresAt.After(now) {
			findErr = service.storage.RemovePart(upload.PartName)
			if findErr == nil {
				actorUserID := upload.UserID
				findErr = service.runConsistencyRepair(ctx, func(repairContext context.Context) error {
					return service.repository.ExpireWithAudit(repairContext, upload.UserID, upload.ID, now, audit.Event{
						OccurredAt:   now,
						ActorUserID:  &actorUserID,
						Type:         audit.EventUploadCancelled,
						Outcome:      audit.OutcomeSuccess,
						ResourceType: "upload",
						ResourceID:   upload.ID,
						ResourcePath: upload.TargetPath,
						Metadata:     map[string]string{"reason_code": "expired"},
					})
				})
			}
			didClean = findErr == nil
		}
		unlock()
		if findErr != nil && !errors.Is(findErr, ErrUploadNotFound) && !errors.Is(findErr, ErrUploadState) {
			return cleaned, findErr
		}
		if didClean {
			cleaned++
		}
	}
	return cleaned, nil
}

func (service *Service) validatePublishedState(ctx context.Context, upload Upload, state storage.PublicationState, now time.Time) error {
	if !state.DestinationExists || state.DestinationSize != upload.TotalSize {
		markErr := service.repository.MarkIndexUnhealthy(ctx, uploadHealthReason(upload.ID), now)
		return errors.Join(
			files.ErrIndexInconsistent,
			fmt.Errorf("%w: published destination size does not match upload metadata", ErrFinalizationReconciliation),
			markErr,
		)
	}
	return nil
}

func (service *Service) completePublished(
	ctx context.Context,
	upload Upload,
	identity auth.Identity,
	modifiedAt time.Time,
	now time.Time,
	metadata map[string]string,
) error {
	destination, err := storage.ParsePath(upload.TargetPath, false)
	if err != nil {
		return err
	}
	entry, err := files.NewEntry(destination, files.KindFile, upload.TotalSize, modifiedAt, now, "", upload.WholeSHA256)
	if err != nil {
		return err
	}
	event := service.event(identity, upload.ID, upload.TargetPath, audit.EventUploadCompleted, audit.OutcomeSuccess, metadata)
	if err := service.repository.CompleteWithAudit(ctx, upload.UserID, upload.ID, now, entry, event); err != nil {
		markErr := service.repository.MarkIndexUnhealthy(ctx, uploadHealthReason(upload.ID), now)
		return errors.Join(err, markErr)
	}
	return nil
}

// ReconcileFinalizing inspects only durable finalizing upload records. It does
// not walk the storage tree and must finish before HTTP serving begins.
func (service *Service) ReconcileFinalizing(ctx context.Context) (int, error) {
	reconciled := 0
	for reconciled < maximumFinalizingReconcile {
		limit := min(finalizingReconcileBatchSize, maximumFinalizingReconcile-reconciled)
		candidates, err := service.repository.ListFinalizing(ctx, limit)
		if err != nil {
			return reconciled, err
		}
		for _, candidate := range candidates {
			if err := ctx.Err(); err != nil {
				return reconciled, err
			}
			unlock := service.locks.lockExclusive(candidate.ID)
			err := service.reconcileFinalizingUpload(ctx, candidate)
			unlock()
			if err != nil {
				return reconciled, err
			}
			reconciled++
		}
		if len(candidates) < limit {
			return reconciled, nil
		}
	}
	remaining, err := service.repository.ListFinalizing(ctx, 1)
	if err != nil {
		return reconciled, err
	}
	if len(remaining) != 0 {
		return reconciled, fmt.Errorf("%w: more than %d uploads require startup reconciliation", ErrFinalizationReconciliation, maximumFinalizingReconcile)
	}
	return reconciled, nil
}

// HasPendingFinalization reports whether a published or partially published
// upload must be reconciled before metadata can be treated as authoritative.
func (service *Service) HasPendingFinalization(ctx context.Context) (bool, error) {
	uploads, err := service.repository.ListFinalizing(ctx, 1)
	if err != nil {
		return false, err
	}
	return len(uploads) != 0, nil
}

func (service *Service) reconcileFinalizingUpload(ctx context.Context, candidate Upload) error {
	unlockMutation := service.mutations.Lock()
	defer unlockMutation()

	upload, err := service.repository.Find(ctx, candidate.UserID, candidate.ID)
	if errors.Is(err, ErrUploadNotFound) || err == nil && upload.Status != StatusFinalizing {
		return nil
	}
	if err != nil {
		return err
	}

	destination, err := storage.ParsePath(upload.TargetPath, false)
	if err != nil {
		return errors.Join(ErrFinalizationReconciliation, err)
	}

	state, err := service.storage.FinalizationState(upload.PartName, destination)
	if err != nil {
		return err
	}

	now := service.now().UTC()
	switch {
	case state.PartExists && !state.DestinationExists:
		return service.repository.ResetFinalizing(ctx, upload.UserID, upload.ID, now)

	case !state.PartExists && state.DestinationExists:
		if err := service.validatePublishedState(ctx, upload, state, now); err != nil {
			return err
		}

		actorUserID := upload.UserID
		identity := auth.Identity{
			User: auth.User{
				ID:   actorUserID,
				Role: auth.RoleOwner,
			},
		}

		return service.completePublished(
			ctx,
			upload,
			identity,
			state.DestinationModifiedAt,
			now,
			map[string]string{"reason_code": "reconciled"},
		)

	default:
		markErr := service.repository.MarkIndexUnhealthy(ctx, uploadHealthReason(upload.ID), now)
		return errors.Join(
			fmt.Errorf("%w: upload %s has ambiguous filesystem state", ErrFinalizationReconciliation, upload.ID),
			markErr,
		)
	}
}

func (service *Service) RunCleanup(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("upload cleanup interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := service.CleanupExpired(ctx); err != nil && !errors.Is(err, context.Canceled) {
				if errors.Is(err, storage.ErrUnavailable) {
					continue
				}
				return err
			}
		}
	}
}

func (service *Service) verifyAndSyncPart(ctx context.Context, upload Upload) ([]byte, error) {
	partFile, err := service.storage.OpenPart(upload.PartName)
	if err != nil {
		return nil, err
	}
	defer partFile.Close()

	wholeHasher := sha256.New()
	buffer := make([]byte, 128*1024)
	if err := service.repository.ValidateChunks(ctx, upload, func(chunk Chunk) error {
		chunkHasher := sha256.New()
		section := io.NewSectionReader(partFile, chunk.Offset, chunk.Size)
		written, err := io.CopyBuffer(io.MultiWriter(wholeHasher, chunkHasher), &contextReader{ctx: ctx, reader: section}, buffer)
		if err != nil {
			return fmt.Errorf("hash stored upload chunk: %w", err)
		}
		if written != chunk.Size || subtle.ConstantTimeCompare(chunkHasher.Sum(nil), chunk.SHA256) != 1 {
			return ErrChecksumMismatch
		}
		return nil
	}); err != nil {
		return nil, err
	}
	computedChecksum := wholeHasher.Sum(nil)
	if len(upload.WholeSHA256) == sha256.Size && subtle.ConstantTimeCompare(computedChecksum, upload.WholeSHA256) != 1 {
		return nil, ErrChecksumMismatch
	}
	if err := partFile.Sync(); err != nil {
		return nil, fmt.Errorf("sync upload part: %w", err)
	}
	return computedChecksum, nil
}

func (service *Service) runConsistencyRepair(requestContext context.Context, repair func(context.Context) error) error {
	repairContext, cancel := context.WithTimeout(context.WithoutCancel(requestContext), consistencyRepairTimeout)
	defer cancel()
	return repair(repairContext)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func (service *Service) recordSecurityFailure(ctx context.Context, identity auth.Identity, uploadID, reason string) error {
	upload, err := service.repository.Find(ctx, identity.User.ID, uploadID)
	if err != nil {
		return err
	}
	return service.record(ctx, identity, uploadID, upload.TargetPath, audit.EventUploadSecurityFailed, audit.OutcomeFailure, map[string]string{"reason_code": reason})
}

func (service *Service) record(ctx context.Context, identity auth.Identity, uploadID, targetPath, eventType, outcome string, metadata map[string]string) error {
	return service.audit.Record(ctx, service.event(identity, uploadID, targetPath, eventType, outcome, metadata))
}

func (service *Service) event(identity auth.Identity, uploadID, targetPath, eventType, outcome string, metadata map[string]string) audit.Event {
	actorUserID := identity.User.ID
	var actorSessionID *int64
	if identity.Session.ID > 0 {
		value := identity.Session.ID
		actorSessionID = &value
	}
	return audit.Event{
		OccurredAt:     service.now().UTC(),
		ActorUserID:    &actorUserID,
		ActorSessionID: actorSessionID,
		Type:           eventType,
		Outcome:        outcome,
		ResourceType:   "upload",
		ResourceID:     uploadID,
		ResourcePath:   targetPath,
		RequestID:      identity.RequestID,
		RemoteIP:       identity.RemoteIP,
		Metadata:       metadata,
	}
}

func totalChunks(totalSize, chunkSize int64) int64 {
	if totalSize <= 0 || chunkSize <= 0 {
		return 0
	}
	chunks := totalSize / chunkSize
	if totalSize%chunkSize != 0 {
		chunks++
	}
	return chunks
}

func chunkBounds(upload Upload, index int64) (offset, expectedSize int64, err error) {
	if index < 0 || index >= upload.TotalChunks || upload.ChunkSize <= 0 || index > math.MaxInt64/upload.ChunkSize {
		return 0, 0, ErrInvalidUpload
	}
	offset = index * upload.ChunkSize
	remaining := upload.TotalSize - offset
	if remaining <= 0 {
		return 0, 0, ErrInvalidUpload
	}
	expectedSize = upload.ChunkSize
	if remaining < expectedSize {
		expectedSize = remaining
	}
	return offset, expectedSize, nil
}

func allowedChunkSize(chunkSize int64) bool {
	return chunkSize == ChunkSize1MiB || chunkSize == ChunkSize2MiB || chunkSize == ChunkSize4MiB || chunkSize == ChunkSize8MiB || chunkSize == ChunkSize16MiB
}

func newUploadID() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate upload ID: %w", err)
	}
	return hex.EncodeToString(randomBytes), nil
}

func validUploadID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
