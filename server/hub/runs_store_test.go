/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openRunsTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestAgentRunUpsertRoundtrip pins insert → update semantics: the second
// upsert replaces status/payload but must PRESERVE a pending cancel flag —
// losing it would drop a user's cross-process cancel request.
func TestAgentRunUpsertRoundtrip(t *testing.T) {
	store := openRunsTestStore(t)
	ctx := context.Background()

	rec := AgentRunRecord{
		RunID: "run-aaaa-1", Instance: "aaaa", Origin: "gateway",
		Status: "running", Payload: `{"id":"run-aaaa-1"}`,
	}
	if err := store.UpsertAgentRun(ctx, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ok, err := store.RequestAgentRunCancel(ctx, "run-aaaa-1")
	if err != nil || !ok {
		t.Fatalf("cancel request: ok=%v err=%v", ok, err)
	}

	rec.Payload = `{"id":"run-aaaa-1","turn":3}`
	if err := store.UpsertAgentRun(ctx, rec); err != nil {
		t.Fatalf("update: %v", err)
	}

	runs, err := store.ListAgentRuns(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	got := runs[0]
	if got.Payload != rec.Payload {
		t.Errorf("payload not updated: %s", got.Payload)
	}
	if !got.CancelRequested {
		t.Error("upsert must preserve a pending cancel_requested flag")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt must be stamped")
	}
}

// TestAgentRunCancelTerminalIsNoop pins that cancel requests are rejected
// for runs already in a terminal state.
func TestAgentRunCancelTerminalIsNoop(t *testing.T) {
	store := openRunsTestStore(t)
	ctx := context.Background()

	if err := store.UpsertAgentRun(ctx, AgentRunRecord{
		RunID: "run-bbbb-1", Instance: "bbbb", Status: "completed",
		Payload: "{}", EndedAt: time.Now(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	ok, err := store.RequestAgentRunCancel(ctx, "run-bbbb-1")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if ok {
		t.Error("cancel of a terminal run must report false")
	}
	if ok, _ := store.RequestAgentRunCancel(ctx, "run-missing"); ok {
		t.Error("cancel of an unknown run must report false")
	}
}

// TestAgentRunPurge pins the two GC axes: terminal rows past retention and
// running rows whose heartbeat stopped; fresh rows survive.
func TestAgentRunPurge(t *testing.T) {
	store := openRunsTestStore(t)
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour)
	for _, rec := range []AgentRunRecord{
		{RunID: "run-old-done", Instance: "x", Status: "completed", Payload: "{}", UpdatedAt: old},
		{RunID: "run-old-running", Instance: "x", Status: "running", Payload: "{}", UpdatedAt: old},
		{RunID: "run-fresh", Instance: "x", Status: "running", Payload: "{}"},
	} {
		if err := store.UpsertAgentRun(ctx, rec); err != nil {
			t.Fatalf("insert %s: %v", rec.RunID, err)
		}
	}

	n, err := store.PurgeAgentRuns(ctx, time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 purged rows, got %d", n)
	}
	runs, err := store.ListAgentRuns(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "run-fresh" {
		t.Errorf("expected only run-fresh to survive, got %+v", runs)
	}
}
