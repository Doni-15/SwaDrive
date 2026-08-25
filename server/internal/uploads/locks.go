package uploads

import "sync"

type chunkLockEntry struct {
	mutex sync.Mutex
	refs  int
}

type uploadLockEntry struct {
	mutex      sync.RWMutex
	chunkMu    sync.Mutex
	chunkLocks map[int64]*chunkLockEntry
	refs       int
}

type uploadLocks struct {
	mu      sync.Mutex
	entries map[string]*uploadLockEntry
}

func newUploadLocks() *uploadLocks {
	return &uploadLocks{entries: make(map[string]*uploadLockEntry)}
}

// lockChunk permits different indexes to share the upload read lock while a
// short-lived per-index lock makes a retry deterministic. The entire entry is
// removed after its final waiter or holder leaves.
func (locks *uploadLocks) lockChunk(id string, index int64) func() {
	entry := locks.acquire(id)
	entry.mutex.RLock()

	entry.chunkMu.Lock()
	chunkEntry := entry.chunkLocks[index]
	if chunkEntry == nil {
		chunkEntry = &chunkLockEntry{}
		entry.chunkLocks[index] = chunkEntry
	}
	chunkEntry.refs++
	entry.chunkMu.Unlock()

	chunkEntry.mutex.Lock()
	return func() {
		chunkEntry.mutex.Unlock()
		entry.chunkMu.Lock()
		chunkEntry.refs--
		if chunkEntry.refs == 0 {
			delete(entry.chunkLocks, index)
		}
		entry.chunkMu.Unlock()
		entry.mutex.RUnlock()
		locks.release(id, entry)
	}
}

// lockExclusive waits for every in-flight chunk before completion,
// cancellation, or expiration cleanup can mutate upload-wide state.
func (locks *uploadLocks) lockExclusive(id string) func() {
	entry := locks.acquire(id)
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		locks.release(id, entry)
	}
}

func (locks *uploadLocks) acquire(id string) *uploadLockEntry {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	entry := locks.entries[id]
	if entry == nil {
		entry = &uploadLockEntry{chunkLocks: make(map[int64]*chunkLockEntry)}
		locks.entries[id] = entry
	}
	entry.refs++
	return entry
}

func (locks *uploadLocks) release(id string, entry *uploadLockEntry) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(locks.entries, id)
	}
}
