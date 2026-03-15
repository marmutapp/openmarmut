package agent

import (
	"fmt"
	"sync"
	"time"
)

// DefaultFileLockTimeout is the maximum time a worker will wait to acquire a file lock.
const DefaultFileLockTimeout = 30 * time.Second

// FileLock provides per-path mutual exclusion for concurrent file writes.
// Multiple workers sharing a Runtime use this to prevent conflicting modifications.
type FileLock struct {
	mu    sync.Mutex
	locks map[string]*pathLock
}

// pathLock tracks a single file path's lock state.
type pathLock struct {
	mu     sync.Mutex
	holder string // worker name holding the lock (for diagnostics)
}

// NewFileLock creates a new file lock manager.
func NewFileLock() *FileLock {
	return &FileLock{
		locks: make(map[string]*pathLock),
	}
}

// Acquire attempts to lock the given path for the named worker.
// It blocks until the lock is acquired or the timeout expires.
// Returns an error if the timeout is reached.
func (fl *FileLock) Acquire(path, worker string, timeout time.Duration) error {
	fl.mu.Lock()
	pl, ok := fl.locks[path]
	if !ok {
		pl = &pathLock{}
		fl.locks[path] = pl
	}
	fl.mu.Unlock()

	done := make(chan struct{})
	go func() {
		pl.mu.Lock()
		close(done)
	}()

	select {
	case <-done:
		pl.holder = worker
		return nil
	case <-time.After(timeout):
		// Could not acquire in time. We need to clean up the goroutine.
		// The goroutine will eventually acquire the lock and we release it immediately.
		go func() {
			<-done
			pl.mu.Unlock()
		}()
		return fmt.Errorf("filelock.Acquire(%s): timeout after %s (held by %s)", path, timeout, pl.holder)
	}
}

// Release unlocks the given path. Panics if the path was not locked.
func (fl *FileLock) Release(path string) {
	fl.mu.Lock()
	pl, ok := fl.locks[path]
	fl.mu.Unlock()

	if !ok {
		return
	}
	pl.holder = ""
	pl.mu.Unlock()
}

// TryAcquire attempts to lock the path without blocking.
// Returns true if the lock was acquired, false otherwise.
func (fl *FileLock) TryAcquire(path, worker string) bool {
	fl.mu.Lock()
	pl, ok := fl.locks[path]
	if !ok {
		pl = &pathLock{}
		fl.locks[path] = pl
	}
	fl.mu.Unlock()

	acquired := pl.mu.TryLock()
	if acquired {
		pl.holder = worker
	}
	return acquired
}

// Holder returns the name of the worker holding the lock on the given path,
// or an empty string if the path is not locked.
func (fl *FileLock) Holder(path string) string {
	fl.mu.Lock()
	pl, ok := fl.locks[path]
	fl.mu.Unlock()

	if !ok {
		return ""
	}
	return pl.holder
}
