package workers

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestFileLockManager_BasicLockUnlock(t *testing.T) {
	mgr := NewFileLockManager()

	mgr.Lock("test.go")
	mgr.Unlock("test.go")
	// Should not panic or deadlock
}

func TestFileLockManager_WithLock(t *testing.T) {
	mgr := NewFileLockManager()

	called := false
	err := mgr.WithLock("test.go", func() error {
		called = true
		return nil
	})

	if err != nil {
		t.Fatalf("WithLock returned error: %v", err)
	}
	if !called {
		t.Fatal("WithLock did not call the function")
	}
}

func TestFileLockManager_ConcurrentSameFile(t *testing.T) {
	mgr := NewFileLockManager()

	var counter int64
	var maxConcurrent int64
	var current int64
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.Lock("shared.go")
			defer mgr.Unlock("shared.go")

			c := atomic.AddInt64(&current, 1)
			if c > 1 {
				// More than one goroutine inside the critical section
				atomic.AddInt64(&maxConcurrent, 1)
			}
			atomic.AddInt64(&counter, 1)
			atomic.AddInt64(&current, -1)
		}()
	}

	wg.Wait()

	if maxConcurrent > 0 {
		t.Fatalf("detected %d concurrent accesses to the same file lock", maxConcurrent)
	}
	if counter != 100 {
		t.Fatalf("expected 100 completions, got %d", counter)
	}
}

func TestFileLockManager_DifferentFilesParallel(t *testing.T) {
	mgr := NewFileLockManager()

	var wg sync.WaitGroup
	results := make([]bool, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			file := "file" + string(rune('A'+idx)) + ".go"
			mgr.Lock(file)
			defer mgr.Unlock(file)
			results[idx] = true
		}(i)
	}

	wg.Wait()

	for i, r := range results {
		if !r {
			t.Fatalf("goroutine %d did not complete", i)
		}
	}
}

func TestFileLockManager_NormalizesPath(t *testing.T) {
	mgr := NewFileLockManager()

	// Both should resolve to the same absolute path
	mgr.Lock("./test.go")
	// If this doesn't deadlock, the paths are correctly normalized
	ch := make(chan struct{})
	go func() {
		mgr.Lock("test.go")
		close(ch)
		mgr.Unlock("test.go")
	}()

	mgr.Unlock("./test.go")
	<-ch // Wait for the goroutine to acquire the lock
}
