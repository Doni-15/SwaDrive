package storage

import "sync"

// MutationCoordinator serializes visible-filesystem mutations through their
// following SQLite index/audit commit. This process-local boundary prevents a
// move or trash operation from interleaving between upload publication and its
// metadata commit. It does not affect chunk writes or downloads.
type MutationCoordinator struct {
	mu sync.Mutex
}

func NewMutationCoordinator() *MutationCoordinator {
	return &MutationCoordinator{}
}

func (coordinator *MutationCoordinator) Lock() func() {
	coordinator.mu.Lock()
	return coordinator.mu.Unlock
}
