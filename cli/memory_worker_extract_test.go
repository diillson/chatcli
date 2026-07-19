/*
 * ChatCLI - Memory worker extraction gate tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the maybeExtract entry: the single-read history snapshot, the
 * watermark clamp when the history shrank behind the worker (pairing
 * repair / compaction / park-resume restore), and the happy extraction
 * path over the snapshot.
 */
package cli

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestMaybeExtract_ClampsWatermarkWhenHistoryShrank(t *testing.T) {
	active := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, active)

	mw.cli.history = []models.Message{{Role: "user", Content: "only one"}}
	mw.mu.Lock()
	mw.lastProcessedIdx = 40 // stale watermark from a longer, pre-repair history
	mw.mu.Unlock()

	mw.maybeExtract(context.Background())

	mw.mu.Lock()
	got := mw.lastProcessedIdx
	mw.mu.Unlock()
	if got != 1 {
		t.Fatalf("watermark = %d, want clamped to len(history) = 1", got)
	}
	if active.calls.Load() != 0 {
		t.Fatal("no extraction may run for a zero delta — the clamp must gate it out")
	}
}

func TestMaybeExtract_ExtractsSnapshotAndAdvancesWatermark(t *testing.T) {
	active := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, active)

	mw.cli.history = []models.Message{
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "u3"},
	}

	mw.maybeExtract(context.Background())

	if active.calls.Load() == 0 {
		t.Fatal("extraction LLM call must have run for a 5-message delta")
	}
	mw.mu.Lock()
	got := mw.lastProcessedIdx
	mw.mu.Unlock()
	if got != 5 {
		t.Fatalf("watermark = %d, want advanced to 5 after successful extraction", got)
	}
}
