package workers

import (
	"path/filepath"
	"sync"
)

// FileLockManager provides per-file mutual exclusion for concurrent writes.
// Read operations do not need locks; only write/patch operations acquire them.
type FileLockManager struct {
	locks map[string]*sync.Mutex
	mu    sync.Mutex
}

// NewFileLockManager creates a new FileLockManager.
func NewFileLockManager() *FileLockManager {
	return &FileLockManager{
		locks: make(map[string]*sync.Mutex),
	}
}

// getLock returns or creates the mutex for a normalized file path.
func (m *FileLockManager) getLock(path string) *sync.Mutex {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	lock, ok := m.locks[abs]
	if !ok {
		lock = &sync.Mutex{}
		m.locks[abs] = lock
	}
	return lock
}

// Lock acquires the mutex for a given file path.
func (m *FileLockManager) Lock(path string) {
	m.getLock(path).Lock()
}

// Unlock releases the mutex for a given file path.
func (m *FileLockManager) Unlock(path string) {
	m.getLock(path).Unlock()
}

// WithLock runs fn while holding the lock for path.
// The lock is always released, even if fn panics.
func (m *FileLockManager) WithLock(path string, fn func() error) error {
	lock := m.getLock(path)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}
