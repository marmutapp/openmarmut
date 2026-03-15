package agent

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileLock_AcquireRelease(t *testing.T) {
	fl := NewFileLock()

	err := fl.Acquire("foo.go", "worker-1", 5*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "worker-1", fl.Holder("foo.go"))

	fl.Release("foo.go")
	assert.Equal(t, "", fl.Holder("foo.go"))
}

func TestFileLock_TryAcquire(t *testing.T) {
	fl := NewFileLock()

	ok := fl.TryAcquire("bar.go", "worker-1")
	require.True(t, ok)
	assert.Equal(t, "worker-1", fl.Holder("bar.go"))

	// Second TryAcquire should fail because lock is held.
	ok = fl.TryAcquire("bar.go", "worker-2")
	assert.False(t, ok)

	fl.Release("bar.go")

	// Now it should succeed.
	ok = fl.TryAcquire("bar.go", "worker-2")
	assert.True(t, ok)
	fl.Release("bar.go")
}

func TestFileLock_Timeout(t *testing.T) {
	fl := NewFileLock()

	// Acquire lock with worker-1.
	err := fl.Acquire("timeout.go", "worker-1", 5*time.Second)
	require.NoError(t, err)

	// worker-2 tries to acquire with a very short timeout — should fail.
	err = fl.Acquire("timeout.go", "worker-2", 50*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")

	fl.Release("timeout.go")
}

func TestFileLock_ConcurrentAccess(t *testing.T) {
	fl := NewFileLock()
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := "worker-" + string(rune('A'+id))
			err := fl.Acquire("shared.go", name, 10*time.Second)
			if err != nil {
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			// Simulate work.
			time.Sleep(10 * time.Millisecond)
			fl.Release("shared.go")
		}(i)
	}
	wg.Wait()

	// All 5 workers should have acquired and released the lock.
	assert.Len(t, order, 5)
}

func TestFileLock_DifferentPaths(t *testing.T) {
	fl := NewFileLock()

	// Different paths should not block each other.
	err := fl.Acquire("a.go", "worker-1", 5*time.Second)
	require.NoError(t, err)

	err = fl.Acquire("b.go", "worker-2", 5*time.Second)
	require.NoError(t, err)

	assert.Equal(t, "worker-1", fl.Holder("a.go"))
	assert.Equal(t, "worker-2", fl.Holder("b.go"))

	fl.Release("a.go")
	fl.Release("b.go")
}

func TestFileLock_HolderEmpty(t *testing.T) {
	fl := NewFileLock()
	assert.Equal(t, "", fl.Holder("nonexistent.go"))
}

func TestFileLock_ReleaseUnlocked(t *testing.T) {
	fl := NewFileLock()
	// Should not panic.
	fl.Release("never-locked.go")
}
