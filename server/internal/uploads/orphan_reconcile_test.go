package uploads

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

func TestOrphanPartReconciliationIsBoundedAgeGatedAndNeverPublishes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	known := "11111111111111111111111111111111.part"
	orphan := "22222222222222222222222222222222.part"
	young := "33333333333333333333333333333333.part"
	storageFake := &orphanPartStorageFake{entries: []storage.UploadPartEntry{
		{Name: known, ModifiedAt: now.Add(-48 * time.Hour)},
		{Name: orphan, ModifiedAt: now.Add(-48 * time.Hour)},
		{Name: young, ModifiedAt: now.Add(-time.Hour)},
		{Name: "not-an-upload.part", ModifiedAt: now.Add(-48 * time.Hour)},
	}}
	repository := &orphanPartRepositoryFake{known: map[string]bool{known: true}}
	reconciler := NewOrphanPartReconciler(repository, storageFake, func() time.Time { return now })

	result, err := reconciler.Reconcile(context.Background(), 24*time.Hour, 10, false)
	if err != nil {
		t.Fatalf("Reconcile(dry run) error = %v", err)
	}
	if result.Scanned != 3 || result.Orphans != 1 || result.Removed != 0 || len(storageFake.removed) != 0 {
		t.Fatalf("dry-run result = %+v removed=%v; want 3/1/0", result, storageFake.removed)
	}

	result, err = reconciler.Reconcile(context.Background(), 24*time.Hour, 10, true)
	if err != nil {
		t.Fatalf("Reconcile(apply) error = %v", err)
	}
	if result.Scanned != 3 || result.Orphans != 1 || result.Removed != 1 || !reflect.DeepEqual(storageFake.removed, []string{orphan}) {
		t.Fatalf("apply result = %+v removed=%v; want only old unknown part", result, storageFake.removed)
	}
}

func TestOrphanPartReconciliationFailsBeforeDeletionWhenScanLimitExceeded(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	storageFake := &orphanPartStorageFake{entries: []storage.UploadPartEntry{
		{Name: "11111111111111111111111111111111.part", ModifiedAt: now.Add(-48 * time.Hour)},
		{Name: "22222222222222222222222222222222.part", ModifiedAt: now.Add(-48 * time.Hour)},
		{Name: "33333333333333333333333333333333.part", ModifiedAt: now.Add(-48 * time.Hour)},
	}}
	reconciler := NewOrphanPartReconciler(&orphanPartRepositoryFake{}, storageFake, func() time.Time { return now })
	result, err := reconciler.Reconcile(context.Background(), time.Hour, 2, true)
	if !errors.Is(err, ErrOrphanPartScanLimit) {
		t.Fatalf("Reconcile() error = %v; want ErrOrphanPartScanLimit", err)
	}
	if result != (OrphanPartReconcileResult{}) || len(storageFake.removed) != 0 {
		t.Fatalf("limit failure result = %+v removed=%v; want no partial deletion", result, storageFake.removed)
	}
}

type orphanPartStorageFake struct {
	entries []storage.UploadPartEntry
	removed []string
}

func (storageFake *orphanPartStorageFake) WalkUploadPartsForReconciliation(ctx context.Context, visit func(storage.UploadPartEntry) error) error {
	for _, entry := range storageFake.entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(entry); err != nil {
			return err
		}
	}
	return nil
}

func (storageFake *orphanPartStorageFake) RemovePart(name string) error {
	storageFake.removed = append(storageFake.removed, name)
	return nil
}

type orphanPartRepositoryFake struct {
	known map[string]bool
}

func (repository *orphanPartRepositoryFake) IsKnownPart(_ context.Context, partName string) (bool, error) {
	return repository.known[partName], nil
}
