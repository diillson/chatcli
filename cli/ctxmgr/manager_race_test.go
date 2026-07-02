/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestBuildPromptMessagesConcurrentIsRaceFree pins read-path thread safety
// under the race detector. Prompt assembly runs on every turn, and concurrent
// turns are reachable (gateway daemon handling a message while a MoA panel
// builds participant prompts over the same session). BuildPromptMessages used
// to sort the SHARED attachments slice in place while holding only the READ
// lock — a latent hazard: it never raced in practice only because attach
// happens to keep the list pre-sorted, an invariant this read path has no
// business leaning on. The method now copies before sorting; this test is the
// guard-rail that keeps concurrent prompt assembly clean under -race.
func TestBuildPromptMessagesConcurrentIsRaceFree(t *testing.T) {
	m := newTestManager(t)

	// Several contexts with distinct priorities so the sort actually moves
	// elements (a pre-sorted list can hide the mutation).
	for i := 0; i < 6; i++ {
		fc := &FileContext{
			ID:        uuid.New().String(),
			Name:      fmt.Sprintf("ctx-%d", i),
			Mode:      ModeFull,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		m.contexts[fc.ID] = fc
		if aerr := m.AttachContext("sess", fc.ID, 10-i); aerr != nil { // reversed priorities
			t.Fatal(aerr)
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if _, berr := m.BuildPromptMessages("sess", FormatOptions{}); berr != nil {
					t.Error(berr)
					return
				}
			}
		}()
	}
	wg.Wait()
}
