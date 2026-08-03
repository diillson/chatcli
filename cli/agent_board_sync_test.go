/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/board"
)

// TestBuildBoardSyncNotice pins the mechanical reconciliation: a doing-card
// whose linked run finished produces one [BOARD SYNC] nudge with the real
// card ID; live runs, unknown runs and repeated states produce none.
func TestBuildBoardSyncNotice(t *testing.T) {
	store := board.NewStore(filepath.Join(t.TempDir(), "board.json"))
	origBoard := squadBoard
	squadBoard = func() *board.Store { return store }
	defer func() { squadBoard = origBoard }()

	reg := runs.NewRegistry(10)
	origReg := agentRunsRegistry
	agentRunsRegistry = func() *runs.Registry { return reg }
	defer func() { agentRunsRegistry = origReg }()

	a := &AgentMode{parallelMode: true}

	// Card in doing linked to a LIVE run — no nudge.
	card, err := store.Create("Implement API", "", "coder", board.ColDoing)
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	runCtx, live := reg.Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder"})
	_ = runCtx
	if _, err := store.LinkRun(card.ID, live.ID()); err != nil {
		t.Fatalf("link run: %v", err)
	}
	if notice := a.buildBoardSyncNotice(); notice != "" {
		t.Fatalf("live run must not trigger a nudge, got:\n%s", notice)
	}

	// Run finishes — one nudge, carrying the real card ID and run ID.
	live.End(nil)
	notice := a.buildBoardSyncNotice()
	if notice == "" {
		t.Fatal("finished run with card still in doing must trigger the nudge")
	}
	if !strings.Contains(notice, "[BOARD SYNC]") || !strings.Contains(notice, card.ID) || !strings.Contains(notice, live.ID()) {
		t.Errorf("nudge must carry the real card and run IDs:\n%s", notice)
	}

	// Same stale state again — deduped, no second nudge.
	if again := a.buildBoardSyncNotice(); again != "" {
		t.Errorf("identical stale state must not re-nudge, got:\n%s", again)
	}

	// Card moved forward — state clears and dedup resets.
	if _, err := store.Move(card.ID, board.ColReview, "orchestrator"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if notice := a.buildBoardSyncNotice(); notice != "" {
		t.Errorf("reconciled board must not nudge, got:\n%s", notice)
	}
}

// TestBuildBoardSyncNoticeScope pins the guards: outside squad mode or with
// cards lacking linked runs there is never a nudge.
func TestBuildBoardSyncNoticeScope(t *testing.T) {
	store := board.NewStore(filepath.Join(t.TempDir(), "board.json"))
	origBoard := squadBoard
	squadBoard = func() *board.Store { return store }
	defer func() { squadBoard = origBoard }()

	if _, err := store.Create("no runs linked", "", "coder", board.ColDoing); err != nil {
		t.Fatalf("create: %v", err)
	}

	seq := &AgentMode{parallelMode: false}
	if notice := seq.buildBoardSyncNotice(); notice != "" {
		t.Error("sequential mode must never nudge")
	}
	par := &AgentMode{parallelMode: true}
	if notice := par.buildBoardSyncNotice(); notice != "" {
		t.Error("cards without linked runs must not nudge")
	}
}
