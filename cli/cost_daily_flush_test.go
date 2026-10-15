/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Once FlushDailySpend returns, the debounced write scheduled before it can
// no longer touch the file: a surface that settled its spend on exit must
// not have its store written after it shut down.
func TestFlushDailySpend_CancelsTheDebouncedWrite(t *testing.T) {
	ct := NewCostTracker()
	ct.storeDir = t.TempDir()
	ct.mu.Lock()
	ct.totalCostUSD = 1.25
	ct.accrueDailyLocked() // schedules the debounced write
	ct.mu.Unlock()

	ct.FlushDailySpend()
	path := filepath.Join(ct.storeDir, dailySpendFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("flush wrote nothing: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(dailySpendSaveDelay + 500*time.Millisecond)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the debounced write ran after the flush")
	}
}
