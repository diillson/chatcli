/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

// i18n is initialized by TestMain in config_sections_test.go.

// TestNudgeSegment_QueuesAndExtractsOwnedTurn pins the RPC memory path: a
// headless turn is handed over as an owned WAL segment (cli.history is
// restored right after the call, so the live-delta path can never see it)
// and the extraction pass consumes it from the queue.
func TestNudgeSegment_QueuesAndExtractsOwnedTurn(t *testing.T) {
	extractor := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, extractor)

	// One turn (2 messages) sits below the min-new-messages cadence gate (4):
	// it must be durably queued but not yet extracted — same rhythm as the
	// REPL, which extracts every couple of turns.
	segment := []models.Message{
		{Role: "user", Content: "mcp user turn"},
		{Role: "assistant", Content: "mcp assistant turn"},
	}
	mw.nudgeSegment(context.Background(), segment)
	if got := len(mw.pendingFiles()); got != 1 {
		t.Fatalf("first turn must be queued (below cadence gate); got %d files", got)
	}
	if extractor.calls.Load() != 0 {
		t.Fatal("first turn must not trigger extraction yet (cadence gate)")
	}

	// The second turn pushes the queued backlog to 4 messages — extraction
	// fires and drains the whole queue.
	mw.nudgeSegment(context.Background(), segment)

	// Generous ceiling: extraction runs on a background goroutine and CI
	// executes this suite under -race with full -coverpkg instrumentation
	// on shared runners — 3s flaked there while the loop exits in
	// milliseconds on a healthy run.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if extractor.calls.Load() > 0 {
			break // extraction consumed the queued segment
		}
		if time.Now().After(deadline) {
			if len(mw.pendingFiles()) > 0 {
				t.Fatal("segment stayed queued but extraction never ran")
			}
			t.Fatal("segment neither queued nor extracted")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// After a successful extraction the queue must be empty.
	deadline = time.Now().Add(15 * time.Second)
	for len(mw.pendingFiles()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("queue not drained after successful extraction: %v", mw.pendingFiles())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestNudgeSegment_EmptyAndNoStoreAreNoops guards the cheap paths.
func TestNudgeSegment_EmptyAndNoStoreAreNoops(t *testing.T) {
	extractor := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, extractor)

	mw.nudgeSegment(context.Background(), nil)
	if got := len(mw.pendingFiles()); got != 0 {
		t.Fatalf("empty segment must not queue anything; got %d files", got)
	}

	mw.cli.memoryStore = nil
	mw.nudgeSegment(context.Background(), []models.Message{{Role: "user", Content: "x"}})
	if got := len(mw.pendingFiles()); got != 0 {
		t.Fatalf("no memory store must be a no-op; got %d files", got)
	}
}
