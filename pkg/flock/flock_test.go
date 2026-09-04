/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package flock

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLock_SerializesCriticalSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	var wg sync.WaitGroup
	inside, maxInside := 0, 0
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := Lock(path)
			mu.Lock()
			inside++
			if inside > maxInside {
				maxInside = inside
			}
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			inside--
			mu.Unlock()
			unlock()
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("critical sections overlapped: %d", maxInside)
	}
	Lock("")() // empty path is a no-op
}
