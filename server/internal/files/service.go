// Package files implements owner-authorized logical file operations.
package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/audit"
	"github.com/Doni-15/SwaDrive/server/internal/auth"
	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

const (
	DefaultTrashLimit          = 100
	MaximumTrashLimit          = 200
	ReconciliationBatchSize    = 100
	MaximumReconciliationRun   = 1000
	DefaultConcurrentDownloads = 32
	MaximumConcurrentDownloads = 256
)

var (
	ErrOwnerRequired      = errors.New("owner role required")
	ErrTrashEntryNotFound = errors.New("trash entry not found")
	ErrTrashState         = errors.New("trash entry is not in the required state")
	ErrReconciliation     = errors.New("trash reconciliation requires manual review")
	ErrDownloadBusy       = errors.New("download queue is full")
)

type TrashState string

const (
	TrashStateTrashing  TrashState = "trashing"
	TrashStateTrashed   TrashState = "trashed"
	TrashStateRestoring TrashState = "restoring"
)

type TrashEntry struct {
	ID           string
	UserID       int64
	OriginalPath string
	TrashName    string
	TrashedAt    time.Time
	State        TrashState
	UpdatedAt    time.Time
}

type TrashRepository interface {
	PrepareTrash(ctx context.Context, entry TrashEntry) error
	AbortTrash(ctx context.Context, userID int64, id string) error
	CommitTrashWithAudit(ctx context.Context, userID int64, id string, updatedAt time.Time, event audit.Event) error
	List(ctx context.Context, userID int64, limit int) ([]TrashEntry, error)
	BeginRestore(ctx context.Context, userID int64, id string, updatedAt time.Time) (TrashEntry, error)
	RollbackRestore(ctx context.Context, userID int64, id string, updatedAt time.Time) error
	FinishRestoreWithAudit(ctx context.Context, userID int64, id string, event audit.Event) error
	ListReconciliation(ctx context.Context, limit int) ([]TrashEntry, error)
}

type Storage interface {
	CreateDirectory(path storage.Path) error
	RemoveEmptyDirectory(path storage.Path) error
	Move(source, destination storage.Path) error
	MoveToTrash(source storage.Path, trashName string) error
	RestoreFromTrash(trashName string, destination storage.Path) error
	TrashState(trashName string, destination storage.Path) (trashExists, destinationExists bool, err error)
	OpenDownload(path storage.Path) (*os.File, storage.Entry, error)
}

type AuditRecorder interface {
	Record(ctx context.Context, event audit.Event) error
}

type Service struct {
	storage            Storage
	trash              TrashRepository
	index              FileIndexRepository
	mutations          *storage.MutationCoordinator
	audit              AuditRecorder
	now                func() time.Time
	downloadSlots      chan struct{}
	downloadAdmissions chan struct{}
}

func NewService(storageManager Storage, trashRepository TrashRepository, indexRepository FileIndexRepository, mutationCoordinator *storage.MutationCoordinator, auditRecorder AuditRecorder, maxConcurrentDownloads int, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if maxConcurrentDownloads < 1 || maxConcurrentDownloads > MaximumConcurrentDownloads {
		maxConcurrentDownloads = DefaultConcurrentDownloads
	}
	if mutationCoordinator == nil {
		mutationCoordinator = storage.NewMutationCoordinator()
	}
	return &Service{
		storage: storageManager, trash: trashRepository, index: indexRepository, mutations: mutationCoordinator, audit: auditRecorder, now: now,
		downloadSlots:      make(chan struct{}, maxConcurrentDownloads),
		downloadAdmissions: make(chan struct{}, maxConcurrentDownloads*4),
	}
}

func (service *Service) List(ctx context.Context, identity auth.Identity, pathValue string, limit int, cursorValue string) (Page, error) {
	if err := requireOwner(identity); err != nil {
		return Page{}, err
	}
	logicalPath, err := storage.ParsePath(pathValue, true)
	if err != nil {
		return Page{}, err
	}
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaximumListLimit {
		return Page{}, ErrInvalidSearch
	}
	cursor, err := decodeCursor(cursorValue, "list", logicalPath.String())
	if err != nil {
		return Page{}, err
	}
	entries, err := service.index.List(ctx, logicalPath.String(), limit+1, cursor)
	if err != nil {
		return Page{}, err
	}
	page := Page{Entries: entries}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		last := page.Entries[len(page.Entries)-1]
		page.NextCursor = encodeCursor(indexCursor{Mode: "list", Scope: logicalPath.String(), Primary: last.NormalizedName, Secondary: last.Name, Tertiary: last.Path})
	}
	return page, nil
}

func (service *Service) Metadata(ctx context.Context, identity auth.Identity, pathValue string) (Entry, error) {
	if err := requireOwner(identity); err != nil {
		return Entry{}, err
	}
	logicalPath, err := storage.ParsePath(pathValue, true)
	if err != nil {
		return Entry{}, err
	}
	if logicalPath.String() == "" {
		if err := service.index.CheckHealthy(ctx); err != nil {
			return Entry{}, err
		}
		return Entry{Path: "", Name: "", Kind: KindDirectory, ModifiedAt: time.Unix(0, 0).UTC()}, nil
	}
	return service.index.Metadata(ctx, logicalPath.String())
}

func (service *Service) CreateDirectory(ctx context.Context, identity auth.Identity, pathValue string) (Entry, error) {
	if err := requireOwner(identity); err != nil {
		return Entry{}, err
	}
	logicalPath, err := storage.ParsePath(pathValue, false)
	if err != nil {
		return Entry{}, err
	}
	unlockMutation := service.mutations.Lock()
	defer unlockMutation()
	if err := service.index.CheckHealthy(ctx); err != nil {
		return Entry{}, err
	}
	if err := service.storage.CreateDirectory(logicalPath); err != nil {
		return Entry{}, err
	}
	now := service.now().UTC()
	entry, err := NewEntry(logicalPath, KindDirectory, 0, now, now, "", nil)
	if err != nil {
		return Entry{}, err
	}
	if err := service.index.CreateWithAudit(ctx, entry, service.event(identity, audit.EventFolderCreated, "folder", "", logicalPath.String(), "", nil)); err != nil {
		compensationErr := service.storage.RemoveEmptyDirectory(logicalPath)
		if compensationErr != nil {
			markErr := service.index.MarkUnhealthy(ctx, "mkdir-compensation", now)
			return Entry{}, errors.Join(ErrIndexInconsistent, err, compensationErr, markErr)
		}
		return Entry{}, err
	}
	return entry, nil
}

func (service *Service) Move(ctx context.Context, identity auth.Identity, sourceValue, destinationValue string) error {
	if err := requireOwner(identity); err != nil {
		return err
	}
	source, err := storage.ParsePath(sourceValue, false)
	if err != nil {
		return err
	}
	destination, err := storage.ParsePath(destinationValue, false)
	if err != nil {
		return err
	}
	unlockMutation := service.mutations.Lock()
	defer unlockMutation()
	if err := service.index.CheckHealthy(ctx); err != nil {
		return err
	}
	if err := service.storage.Move(source, destination); err != nil {
		return err
	}
	if err := service.index.MoveSubtreeWithAudit(ctx, source, destination, service.event(identity, audit.EventFileMoved, "file", "", source.String(), destination.String(), nil)); err != nil {
		compensationErr := service.storage.Move(destination, source)
		if compensationErr != nil {
			markErr := service.index.MarkUnhealthy(ctx, "move-compensation", service.now().UTC())
			return errors.Join(ErrIndexInconsistent, err, compensationErr, markErr)
		}
		return err
	}
	return nil
}

func (service *Service) Trash(ctx context.Context, identity auth.Identity, pathValue string) (TrashEntry, error) {
	if err := requireOwner(identity); err != nil {
		return TrashEntry{}, err
	}
	logicalPath, err := storage.ParsePath(pathValue, false)
	if err != nil {
		return TrashEntry{}, err
	}
	unlockMutation := service.mutations.Lock()
	defer unlockMutation()
	id, err := randomID()
	if err != nil {
		return TrashEntry{}, err
	}
	entry := TrashEntry{
		ID:           id,
		UserID:       identity.User.ID,
		OriginalPath: logicalPath.String(),
		TrashName:    id,
		TrashedAt:    service.now().UTC(),
		State:        TrashStateTrashing,
		UpdatedAt:    service.now().UTC(),
	}

	if err := service.trash.PrepareTrash(ctx, entry); err != nil {
		return TrashEntry{}, err
	}
	if err := service.storage.MoveToTrash(logicalPath, entry.TrashName); err != nil {
		return TrashEntry{}, errors.Join(err, service.trash.AbortTrash(ctx, entry.UserID, entry.ID))
	}
	if err := service.trash.CommitTrashWithAudit(ctx, entry.UserID, entry.ID, entry.UpdatedAt, service.event(identity, audit.EventFileTrashed, "trash_entry", entry.ID, logicalPath.String(), "", nil)); err != nil {
		markErr := service.index.MarkUnhealthy(ctx, trashHealthReason(entry.ID), service.now().UTC())
		return TrashEntry{}, errors.Join(err, markErr)
	}
	entry.State = TrashStateTrashed
	return entry, nil
}

func (service *Service) ListTrash(ctx context.Context, identity auth.Identity, limit int) ([]TrashEntry, error) {
	if err := requireOwner(identity); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = DefaultTrashLimit
	}
	if limit < 1 || limit > MaximumTrashLimit {
		return nil, ErrInvalidSearch
	}
	return service.trash.List(ctx, identity.User.ID, limit)
}

func (service *Service) Restore(ctx context.Context, identity auth.Identity, id string) error {
	if err := requireOwner(identity); err != nil {
		return err
	}
	if !validResourceID(id) {
		return ErrTrashEntryNotFound
	}
	unlockMutation := service.mutations.Lock()
	defer unlockMutation()
	now := service.now().UTC()
	entry, err := service.trash.BeginRestore(ctx, identity.User.ID, id, now)
	if err != nil {
		return err
	}
	destination, err := storage.ParsePath(entry.OriginalPath, false)
	if err != nil {
		return err
	}
	if err := service.storage.RestoreFromTrash(entry.TrashName, destination); err != nil {
		return errors.Join(err, service.trash.RollbackRestore(ctx, identity.User.ID, id, now))
	}
	err = service.trash.FinishRestoreWithAudit(ctx, identity.User.ID, id, service.event(identity, audit.EventFileRestored, "trash_entry", entry.ID, entry.OriginalPath, "", nil))
	if err != nil {
		markErr := service.index.MarkUnhealthy(ctx, restoreHealthReason(entry.ID), service.now().UTC())
		return errors.Join(err, markErr)
	}
	return nil
}

// ReconcileTrash resolves durable trash operations left between their
// filesystem and SQLite steps. Ambiguous states stop startup for manual review
// instead of guessing which copy is authoritative.
func (service *Service) ReconcileTrash(ctx context.Context) (int, error) {
	reconciled := 0
	for reconciled < MaximumReconciliationRun {
		limit := min(ReconciliationBatchSize, MaximumReconciliationRun-reconciled)
		entries, err := service.trash.ListReconciliation(ctx, limit)
		if err != nil {
			return reconciled, err
		}
		for _, entry := range entries {
			if err := service.reconcileTrashEntry(ctx, entry); err != nil {
				return reconciled, err
			}
			reconciled++
		}
		if len(entries) < limit {
			return reconciled, nil
		}
	}
	remaining, err := service.trash.ListReconciliation(ctx, 1)
	if err != nil {
		return reconciled, err
	}
	if len(remaining) != 0 {
		return reconciled, fmt.Errorf("%w: more than %d entries require startup reconciliation", ErrReconciliation, MaximumReconciliationRun)
	}
	return reconciled, nil
}

func (service *Service) reconcileTrashEntry(ctx context.Context, entry TrashEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlockMutation := service.mutations.Lock()
	defer unlockMutation()
	destination, err := storage.ParsePath(entry.OriginalPath, false)
	if err != nil {
		return errors.Join(ErrReconciliation, err)
	}
	trashExists, destinationExists, err := service.storage.TrashState(entry.TrashName, destination)
	if err != nil {
		return err
	}
	now := service.now().UTC()
	event := service.reconciliationEvent(entry, now)
	switch {
	case entry.State == TrashStateTrashing && trashExists && !destinationExists:
		return service.trash.CommitTrashWithAudit(ctx, entry.UserID, entry.ID, now, event)
	case entry.State == TrashStateTrashing && !trashExists && destinationExists:
		return service.trash.AbortTrash(ctx, entry.UserID, entry.ID)
	case entry.State == TrashStateRestoring && !trashExists && destinationExists:
		return service.trash.FinishRestoreWithAudit(ctx, entry.UserID, entry.ID, event)
	case entry.State == TrashStateRestoring && trashExists && !destinationExists:
		return service.trash.RollbackRestore(ctx, entry.UserID, entry.ID, now)
	default:
		return fmt.Errorf("%w: trash entry %s has ambiguous filesystem state", ErrReconciliation, entry.ID)
	}
}

func (service *Service) Search(ctx context.Context, identity auth.Identity, query string, limit int, cursorValue string) (SearchPage, error) {
	if err := requireOwner(identity); err != nil {
		return SearchPage{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > MaximumSearchQueryBytes {
		return SearchPage{}, ErrInvalidSearch
	}
	if limit == 0 {
		limit = DefaultSearchLimit
	}
	if limit < 1 || limit > MaximumSearchLimit {
		return SearchPage{}, ErrInvalidSearch
	}
	normalizedQuery := normalizeSearchValue(query)
	cursor, err := decodeCursor(cursorValue, "search", normalizedQuery)
	if err != nil {
		return SearchPage{}, err
	}
	entries, err := service.index.Search(ctx, normalizedQuery, limit+1, cursor)
	if err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{Entries: entries}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		last := page.Entries[len(page.Entries)-1]
		page.NextCursor = encodeCursor(indexCursor{Mode: "search", Scope: normalizedQuery, Primary: last.NormalizedPath, Secondary: last.Path})
	}
	return page, nil
}

type Download struct {
	*os.File
	releaseOnce sync.Once
	release     func()
}

func (download *Download) Close() error {
	err := download.File.Close()
	download.releaseOnce.Do(download.release)
	return err
}

func (service *Service) OpenDownload(ctx context.Context, identity auth.Identity, pathValue string) (*Download, Entry, error) {
	if err := requireOwner(identity); err != nil {
		return nil, Entry{}, err
	}
	select {
	case service.downloadAdmissions <- struct{}{}:
		defer func() { <-service.downloadAdmissions }()
	default:
		return nil, Entry{}, ErrDownloadBusy
	}
	select {
	case service.downloadSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, Entry{}, ctx.Err()
	}
	release := func() { <-service.downloadSlots }
	logicalPath, err := storage.ParsePath(pathValue, false)
	if err != nil {
		release()
		return nil, Entry{}, err
	}
	entry, err := service.index.Metadata(ctx, logicalPath.String())
	if err != nil {
		release()
		return nil, Entry{}, err
	}
	if entry.Kind != KindFile {
		release()
		return nil, Entry{}, storage.ErrNotRegularFile
	}
	file, physicalEntry, err := service.storage.OpenDownload(logicalPath)
	if err != nil {
		release()
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrSymlink) || errors.Is(err, storage.ErrNotRegularFile) {
			markErr := service.index.MarkUnhealthy(ctx, "download-open", service.now().UTC())
			return nil, Entry{}, errors.Join(ErrIndexInconsistent, err, markErr)
		}
		return nil, Entry{}, err
	}
	if physicalEntry.Size != entry.Size || physicalEntry.ModifiedAt.UTC().Unix() != entry.ModifiedAt.UTC().Unix() {
		_ = file.Close()
		release()
		markErr := service.index.MarkUnhealthy(ctx, "download-metadata", service.now().UTC())
		return nil, Entry{}, errors.Join(ErrIndexInconsistent, markErr)
	}
	if err := service.audit.Record(ctx, service.event(identity, audit.EventFileDownloaded, "file", "", logicalPath.String(), "", nil)); err != nil {
		_ = file.Close()
		release()
		return nil, Entry{}, err
	}
	return &Download{File: file, release: release}, entry, nil
}

func (service *Service) event(identity auth.Identity, eventType, resourceType, resourceID, resourcePath, destinationPath string, metadata map[string]string) audit.Event {
	actorUserID := identity.User.ID
	actorSessionID := identity.Session.ID
	return audit.Event{
		OccurredAt:      service.now().UTC(),
		ActorUserID:     &actorUserID,
		ActorSessionID:  &actorSessionID,
		Type:            eventType,
		Outcome:         audit.OutcomeSuccess,
		ResourceType:    resourceType,
		ResourceID:      resourceID,
		ResourcePath:    resourcePath,
		DestinationPath: destinationPath,
		RequestID:       identity.RequestID,
		RemoteIP:        identity.RemoteIP,
		Metadata:        metadata,
	}
}

func (service *Service) reconciliationEvent(entry TrashEntry, now time.Time) audit.Event {
	actorUserID := entry.UserID
	eventType := audit.EventFileTrashed
	if entry.State == TrashStateRestoring {
		eventType = audit.EventFileRestored
	}
	return audit.Event{
		OccurredAt:   now,
		ActorUserID:  &actorUserID,
		Type:         eventType,
		Outcome:      audit.OutcomeSuccess,
		ResourceType: "trash_entry",
		ResourceID:   entry.ID,
		ResourcePath: entry.OriginalPath,
		Metadata:     map[string]string{"reason_code": "reconciled"},
	}
}

func trashHealthReason(id string) string {
	return "trash:" + id
}

func restoreHealthReason(id string) string {
	return "restore:" + id
}

func requireOwner(identity auth.Identity) error {
	if identity.User.Role != auth.RoleOwner {
		return ErrOwnerRequired
	}
	return nil
}

func randomID() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("generate random ID: %w", err)
	}
	return hex.EncodeToString(randomBytes), nil
}

func validResourceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
