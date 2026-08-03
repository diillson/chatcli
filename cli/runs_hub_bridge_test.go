/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/server/hub"
)

// TestRunsHubBridgeMirrorsAndCancels exercises the full cross-process loop
// with one shared hub.db: a "daemon" registry mirrors its run, a second
// instance sees it through the remote provider, requests cancellation, and
// the owning bridge honors it.
func TestRunsHubBridgeMirrorsAndCancels(t *testing.T) {
	t.Setenv("CHATCLI_HUB_POLL_MS", "50")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := hub.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	reg := runs.NewRegistry(10)
	stop := startRunsHubBridge(ctx, store, reg, nil)
	defer stop()

	runCtx, live := reg.Begin(ctx, runs.Info{Kind: runs.KindWorker, Agent: "coder", Task: "build feature", Origin: "gateway"})
	live.SetTurn(2, 10)

	// The flusher must mirror the run into the store.
	if !waitFor(t, 3*time.Second, func() bool {
		recs, err := store.ListAgentRuns(context.Background())
		return err == nil && len(recs) == 1 && recs[0].RunID == live.ID()
	}) {
		t.Fatal("run was not mirrored into the hub store")
	}
	recs, _ := store.ListAgentRuns(context.Background())
	var mirrored runs.Info
	if err := json.Unmarshal([]byte(recs[0].Payload), &mirrored); err != nil {
		t.Fatalf("payload must be a runs.Info JSON snapshot: %v", err)
	}
	if mirrored.Agent != "coder" || mirrored.Instance != reg.Instance() {
		t.Errorf("mirrored snapshot wrong: %+v", mirrored)
	}

	// A cancel request written by "another process" is honored locally.
	if ok, err := store.RequestAgentRunCancel(context.Background(), live.ID()); err != nil || !ok {
		t.Fatalf("cancel request: ok=%v err=%v", ok, err)
	}
	if !waitFor(t, 3*time.Second, func() bool {
		select {
		case <-runCtx.Done():
			return true
		default:
			return false
		}
	}) {
		t.Fatal("bridge did not honor the cross-process cancel request")
	}
	live.End(runCtx.Err())

	// The terminal state must land in the mirror (final flush path is the
	// ticker here, not shutdown).
	if !waitFor(t, 3*time.Second, func() bool {
		recs, err := store.ListAgentRuns(context.Background())
		return err == nil && len(recs) == 1 && recs[0].Status == string(runs.StatusCancelled)
	}) {
		t.Fatal("terminal status was not mirrored")
	}
}

// TestRemoteRunsProviderFiltersOwnInstance pins that the provider lists only
// OTHER processes' runs and flags heartbeat-silent ones as stale.
func TestRemoteRunsProviderFiltersOwnInstance(t *testing.T) {
	t.Setenv("CHATCLI_HUB_POLL_MS", "50")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, err := hub.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = store.Close() }()

	reg := runs.NewRegistry(10)
	stop := startRunsHubBridge(ctx, store, reg, nil)
	defer stop()

	// A local run must NOT appear in the remote view.
	_, local := reg.Begin(ctx, runs.Info{Kind: runs.KindOrchestrator, Agent: "coder"})
	defer local.End(nil)

	// A foreign run (fresh heartbeat) and a stale foreign run.
	foreign := runs.Info{ID: "run-ffff-1", Instance: "ffff", Kind: runs.KindWorker, Agent: "tester", Status: runs.StatusRunning}
	payload, _ := json.Marshal(foreign)
	if err := store.UpsertAgentRun(ctx, hub.AgentRunRecord{
		RunID: foreign.ID, Instance: foreign.Instance, Status: string(foreign.Status), Payload: string(payload),
	}); err != nil {
		t.Fatalf("insert foreign: %v", err)
	}
	staleInfo := runs.Info{ID: "run-eeee-1", Instance: "eeee", Kind: runs.KindWorker, Agent: "shell", Status: runs.StatusRunning}
	stalePayload, _ := json.Marshal(staleInfo)
	if err := store.UpsertAgentRun(ctx, hub.AgentRunRecord{
		RunID: staleInfo.ID, Instance: staleInfo.Instance, Status: string(staleInfo.Status),
		Payload: string(stalePayload), UpdatedAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("insert stale: %v", err)
	}

	remote := listRemoteAgentRuns()
	if len(remote) != 2 {
		t.Fatalf("expected 2 remote runs, got %d", len(remote))
	}
	byID := map[string]remoteAgentRun{}
	for _, r := range remote {
		if r.Info.Instance == reg.Instance() {
			t.Errorf("remote view must exclude this process's runs: %+v", r.Info)
		}
		byID[r.Info.ID] = r
	}
	if byID["run-ffff-1"].Stale {
		t.Error("fresh foreign run must not be stale")
	}
	if !byID["run-eeee-1"].Stale {
		t.Error("heartbeat-silent foreign run must be flagged stale")
	}
}
