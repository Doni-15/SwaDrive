package uploads

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

const (
	DefaultOrphanPartMinimumAge = 24 * time.Hour
	DefaultOrphanPartScanLimit  = 1_000
	MaximumOrphanPartScanLimit  = 10_000
)

var ErrOrphanPartScanLimit = errors.New("upload part reconciliation scan limit exceeded")

type OrphanPartStorage interface {
	WalkUploadPartsForReconciliation(ctx context.Context, visit func(storage.UploadPartEntry) error) error
	RemovePart(name string) error
}

type OrphanPartRepository interface {
	IsKnownPart(ctx context.Context, partName string) (bool, error)
}

type OrphanPartReconcileResult struct {
	Scanned int
	Orphans int
	Removed int
}

type OrphanPartReconciler struct {
	repository OrphanPartRepository
	storage    OrphanPartStorage
	now        func() time.Time
}

func NewOrphanPartReconciler(repository OrphanPartRepository, storageManager OrphanPartStorage, now func() time.Time) *OrphanPartReconciler {
	if now == nil {
		now = time.Now
	}
	return &OrphanPartReconciler{repository: repository, storage: storageManager, now: now}
}

// Reconcile is an explicit offline-admin operation. It never publishes parts,
// never reads their contents, and deletes only old regular files whose names
// exactly match SwaDrive's random upload-part format and have no database row.
func (reconciler *OrphanPartReconciler) Reconcile(ctx context.Context, minimumAge time.Duration, scanLimit int, apply bool) (OrphanPartReconcileResult, error) {
	if minimumAge <= 0 || scanLimit < 1 || scanLimit > MaximumOrphanPartScanLimit {
		return OrphanPartReconcileResult{}, ErrInvalidUpload
	}
	candidates := make([]storage.UploadPartEntry, 0, min(scanLimit, DefaultOrphanPartScanLimit))
	err := reconciler.storage.WalkUploadPartsForReconciliation(ctx, func(entry storage.UploadPartEntry) error {
		if !validPartName(entry.Name) {
			return nil
		}
		if len(candidates) == scanLimit {
			return ErrOrphanPartScanLimit
		}
		candidates = append(candidates, entry)
		return nil
	})
	if err != nil {
		return OrphanPartReconcileResult{}, err
	}

	result := OrphanPartReconcileResult{Scanned: len(candidates)}
	cutoff := reconciler.now().UTC().Add(-minimumAge)
	orphans := make([]string, 0)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		known, err := reconciler.repository.IsKnownPart(ctx, candidate.Name)
		if err != nil {
			return result, err
		}
		if known || candidate.ModifiedAt.After(cutoff) {
			continue
		}
		result.Orphans++
		orphans = append(orphans, candidate.Name)
	}
	if !apply {
		return result, nil
	}
	for _, name := range orphans {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := reconciler.storage.RemovePart(name); err != nil {
			return result, fmt.Errorf("remove orphan upload part: %w", err)
		}
		result.Removed++
	}
	return result, nil
}

func validPartName(name string) bool {
	if len(name) != 32+len(".part") || !strings.HasSuffix(name, ".part") {
		return false
	}
	identifier := strings.TrimSuffix(name, ".part")
	decoded, err := hex.DecodeString(identifier)
	return err == nil && len(decoded) == 16 && strings.ToLower(identifier) == identifier
}
