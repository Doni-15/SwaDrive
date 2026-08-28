package storage

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const availabilityProbeInterval = 5 * time.Second

// Provider keeps content access behind a verified storage identity. A provider
// can transition from available to unavailable at runtime, but deliberately
// does not recover in-process: startup reconciliation must run before a newly
// returned volume can serve content again.
type Provider struct {
	rootPath         string
	expectedVolumeID string
	available        atomic.Bool
	onStateChange    func(bool)
	mutationMu       sync.Mutex
	probeMu          sync.Mutex
	lastProbe        time.Time
}

// OpenProvider returns an unavailable provider rather than failing when the
// configured content root cannot be opened and verified. It never initializes
// storage directories unless the volume marker has already matched.
func OpenProvider(rootPath, expectedVolumeID string, onStateChange func(bool)) *Provider {
	provider := &Provider{
		rootPath:         rootPath,
		expectedVolumeID: expectedVolumeID,
		onStateChange:    onStateChange,
	}
	manager, err := OpenVerified(rootPath, expectedVolumeID)
	if manager != nil {
		err = errors.Join(err, manager.Close())
	}
	if err == nil {
		provider.available.Store(true)
		provider.lastProbe = time.Now()
	}
	provider.notify(provider.available.Load())
	return provider
}

// Available probes all storage invariants while the provider remains active.
// Once a probe fails, the provider remains unavailable until process restart.
func (provider *Provider) Available() bool {
	if !provider.available.Load() {
		return false
	}
	provider.probeMu.Lock()
	defer provider.probeMu.Unlock()
	if time.Since(provider.lastProbe) < availabilityProbeInterval {
		return true
	}
	provider.lastProbe = time.Now()
	manager, err := provider.storageManager()
	if err != nil {
		return false
	}
	if err := manager.Close(); err != nil {
		provider.markUnavailable()
		return false
	}
	return provider.available.Load()
}

// Probe bypasses the health-probe cache. Content operations validate storage
// independently; this method is intended for explicit operational checks and
// deterministic tests.
func (provider *Provider) Probe() bool {
	if !provider.available.Load() {
		return false
	}
	provider.probeMu.Lock()
	defer provider.probeMu.Unlock()
	provider.lastProbe = time.Now()
	manager, err := provider.storageManager()
	if err != nil {
		return false
	}
	if err := manager.Close(); err != nil {
		provider.markUnavailable()
		return false
	}
	return provider.available.Load()
}

func (provider *Provider) storageManager() (*Manager, error) {
	if !provider.available.Load() {
		return nil, ErrUnavailable
	}
	manager, err := open(provider.rootPath, provider.expectedVolumeID, true, false)
	if err != nil {
		provider.markUnavailable()
		return nil, ErrUnavailable
	}
	return manager, nil
}

func (provider *Provider) finish(operationError error) error {
	if operationError == nil {
		return nil
	}
	if provider.available.Load() {
		if validationError := verifyExisting(provider.rootPath, provider.expectedVolumeID); validationError != nil {
			provider.markUnavailable()
			return errors.Join(ErrUnavailable, operationError)
		}
	}
	if !provider.available.Load() {
		return errors.Join(ErrUnavailable, operationError)
	}
	return operationError
}

func (provider *Provider) markUnavailable() {
	if provider.available.CompareAndSwap(true, false) {
		provider.notify(false)
	}
}

func (provider *Provider) notify(available bool) {
	if provider.onStateChange != nil {
		provider.onStateChange(available)
	}
}

func (provider *Provider) CreateDirectory(path Path) error {
	provider.mutationMu.Lock()
	defer provider.mutationMu.Unlock()
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.CreateDirectory(path))
}

func (provider *Provider) RemoveEmptyDirectory(path Path) error {
	provider.mutationMu.Lock()
	defer provider.mutationMu.Unlock()
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.RemoveEmptyDirectory(path))
}

func (provider *Provider) Move(source, destination Path) error {
	provider.mutationMu.Lock()
	defer provider.mutationMu.Unlock()
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.Move(source, destination))
}

func (provider *Provider) MoveToTrash(source Path, trashName string) error {
	provider.mutationMu.Lock()
	defer provider.mutationMu.Unlock()
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.MoveToTrash(source, trashName))
}

func (provider *Provider) RestoreFromTrash(trashName string, destination Path) error {
	provider.mutationMu.Lock()
	defer provider.mutationMu.Unlock()
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.RestoreFromTrash(trashName, destination))
}

func (provider *Provider) TrashState(trashName string, destination Path) (bool, bool, error) {
	provider.mutationMu.Lock()
	defer provider.mutationMu.Unlock()
	manager, err := provider.storageManager()
	if err != nil {
		return false, false, err
	}
	defer manager.Close()
	trashExists, destinationExists, operationError := manager.TrashState(trashName, destination)
	return trashExists, destinationExists, provider.finish(operationError)
}

func (provider *Provider) OpenDownload(path Path) (*os.File, Entry, error) {
	manager, err := provider.storageManager()
	if err != nil {
		return nil, Entry{}, err
	}
	defer manager.Close()
	file, entry, operationError := manager.OpenDownload(path)
	return file, entry, provider.finish(operationError)
}

func (provider *Provider) PrepareUpload(destination Path) error {
	provider.mutationMu.Lock()
	defer provider.mutationMu.Unlock()
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.PrepareUpload(destination))
}

func (provider *Provider) CreatePart(name string) error {
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.CreatePart(name))
}

func (provider *Provider) OpenPart(name string) (*os.File, error) {
	manager, err := provider.storageManager()
	if err != nil {
		return nil, err
	}
	defer manager.Close()
	file, operationError := manager.OpenPart(name)
	return file, provider.finish(operationError)
}

func (provider *Provider) RemovePart(name string) error {
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.RemovePart(name))
}

func (provider *Provider) PartInfo(name string) (os.FileInfo, error) {
	manager, err := provider.storageManager()
	if err != nil {
		return nil, err
	}
	defer manager.Close()
	info, operationError := manager.PartInfo(name)
	return info, provider.finish(operationError)
}

func (provider *Provider) FinalizePart(partName string, destination Path) error {
	provider.mutationMu.Lock()
	defer provider.mutationMu.Unlock()
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.FinalizePart(partName, destination))
}

func (provider *Provider) FinalizationState(partName string, destination Path) (PublicationState, error) {
	provider.mutationMu.Lock()
	defer provider.mutationMu.Unlock()
	manager, err := provider.storageManager()
	if err != nil {
		return PublicationState{}, err
	}
	defer manager.Close()
	state, operationError := manager.FinalizationState(partName, destination)
	return state, provider.finish(operationError)
}

func (provider *Provider) CheckAvailable(required, reserve uint64) error {
	manager, err := provider.storageManager()
	if err != nil {
		return err
	}
	defer manager.Close()
	return provider.finish(manager.CheckAvailable(required, reserve))
}
