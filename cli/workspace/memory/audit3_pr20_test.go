/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"fmt"
	"sync"
	"testing"

	"go.uber.org/zap"
)

func TestFactIndex_TwoProcessesMergeUnderTheLock(t *testing.T) {
	dir := t.TempDir()
	a := NewFactIndex(dir, DefaultConfig(), zap.NewNop())
	b := NewFactIndex(dir, DefaultConfig(), zap.NewNop())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		// Every fact is lexically unrelated to the others so reconciliation
		// never folds two of them into one.
		go func(i int) {
			defer wg.Done()
			a.AddFact(fmt.Sprintf("ka%d qa%d ra%d sa%d ta%d", i, i*7, i*11, i*13, i*17), "project", nil)
		}(i)
		go func(i int) {
			defer wg.Done()
			b.AddFact(fmt.Sprintf("kb%d qb%d rb%d sb%d tb%d", i, i*7, i*11, i*13, i*17), "project", nil)
		}(i)
	}
	wg.Wait()
	// A third process reads the shared file: every fact both sides wrote
	// must be there — no read-merge-write race lost the loser's entries.
	c := NewFactIndex(dir, DefaultConfig(), zap.NewNop())
	if got := len(c.GetAll()); got != 40 {
		t.Fatalf("facts on disk = %d, want 40", got)
	}
}
