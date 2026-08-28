package uploads

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Doni-15/SwaDrive/server/internal/storage"
)

func TestCleanupWorkerStopsOnContextCancellation(t *testing.T) {
	service := &Service{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.RunCleanup(ctx, time.Hour); err != nil {
		t.Fatalf("RunCleanup(cancelled) error = %v", err)
	}
}

func TestCleanupWorkerStaysAliveWhileStorageIsUnavailable(t *testing.T) {
	environment := newUploadTestEnvironment(t, 1)
	environment.create(t, "expired.bin", 1)
	*environment.now = environment.now.Add(UploadLifetime + time.Second)
	environment.service.storage = unavailableUploadStorage{}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- environment.service.RunCleanup(ctx, time.Millisecond) }()
	select {
	case err := <-result:
		t.Fatalf("RunCleanup() stopped while storage was unavailable: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunCleanup(cancelled) error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunCleanup() did not stop after cancellation")
	}
}

type unavailableUploadStorage struct{}

func (unavailableUploadStorage) PrepareUpload(storage.Path) error { return storage.ErrUnavailable }
func (unavailableUploadStorage) CreatePart(string) error          { return storage.ErrUnavailable }
func (unavailableUploadStorage) OpenPart(string) (*os.File, error) {
	return nil, storage.ErrUnavailable
}
func (unavailableUploadStorage) OpenDownload(storage.Path) (*os.File, storage.Entry, error) {
	return nil, storage.Entry{}, storage.ErrUnavailable
}
func (unavailableUploadStorage) RemovePart(string) error { return storage.ErrUnavailable }
func (unavailableUploadStorage) PartInfo(string) (os.FileInfo, error) {
	return nil, storage.ErrUnavailable
}
func (unavailableUploadStorage) FinalizePart(string, storage.Path) error {
	return storage.ErrUnavailable
}
func (unavailableUploadStorage) FinalizationState(string, storage.Path) (storage.PublicationState, error) {
	return storage.PublicationState{}, storage.ErrUnavailable
}
func (unavailableUploadStorage) CheckAvailable(uint64, uint64) error {
	return storage.ErrUnavailable
}
