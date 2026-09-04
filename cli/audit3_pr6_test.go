/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestTenantSwap_CarriesPerConversationState(t *testing.T) {
	cli := newTenantTestCLI(t)
	ctx := context.Background()
	// Base set: a bootstrap card, an undo snapshot, a recall trace, a
	// forward watermark.
	cli.history = []models.Message{{Role: "user", Content: "base turn"}}
	cli.bootstrapCardState().chat = "BASE CARD"
	cli.preCompaction = [][]models.Message{{{Role: "user", Content: "base undo"}}}
	cli.rememberRecallTrace("base query", nil)
	cli.extForwardState().forwarded = 7
	cli.pendingInboundImages = []models.ImageContent{{Data: []byte("img")}}
	cli.lastAgentReply = "base reply"

	leave := cli.enterTenant(ctx, "telegram:42")
	if leave == nil {
		t.Fatal("tenant must be entered")
	}
	if cli.bootstrapCard != nil && cli.bootstrapCard.chat == "BASE CARD" {
		t.Fatal("the base bootstrap card must not follow into the tenant")
	}
	if len(cli.preCompaction) != 0 || cli.lastRecallTrace != nil || cli.extForwardState().forwarded != 0 || len(cli.pendingInboundImages) != 0 || cli.lastAgentReply != "" {
		t.Fatalf("per-conversation state leaked into the tenant: undo=%d trace=%v fwd=%d images=%d reply=%q",
			len(cli.preCompaction), cli.lastRecallTrace, cli.extForwardState().forwarded, len(cli.pendingInboundImages), cli.lastAgentReply)
	}
	// The base memory worker must keep reading the BASE history while the
	// tenant is active.
	cli.history = []models.Message{{Role: "user", Content: "tenant turn"}}
	if h := cli.baseHistory(); len(h) != 1 || h[0].Content != "base turn" {
		t.Fatalf("base worker must read the base history during a tenant turn: %+v", h)
	}
	cli.bootstrapCardState().chat = "TENANT CARD"
	cli.preCompaction = [][]models.Message{{{Role: "user", Content: "tenant undo"}}}
	leave()
	if cli.bootstrapCardState().chat != "BASE CARD" || len(cli.preCompaction) != 1 || cli.preCompaction[0][0].Content != "base undo" || cli.extForwardState().forwarded != 7 || cli.lastAgentReply != "base reply" {
		t.Fatal("leaving the tenant must restore the base set's state")
	}
	if h := cli.baseHistory(); len(h) != 1 || h[0].Content != "base turn" {
		t.Fatalf("base history after leave: %+v", h)
	}
	// Re-entering the same tenant brings its own state back.
	leave2 := cli.enterTenant(ctx, "telegram:42")
	if cli.bootstrapCardState().chat != "TENANT CARD" || len(cli.preCompaction) != 1 || cli.preCompaction[0][0].Content != "tenant undo" {
		t.Fatal("re-entering a tenant must restore its own state")
	}
	leave2()
}

func TestExternalMemoryTools_CarryTheTenant(t *testing.T) {
	cli := newTenantTestCLI(t)
	f := &fakeToolCaller{answers: map[string]string{"memsvc/memory_recall": "- x"}, fail: map[string]bool{}}
	cli.extToolCaller = f
	t.Setenv(MemoryProviderEnv, "mcp:memsvc")
	leave := cli.enterTenant(context.Background(), "slack:u1")
	_ = cli.externalMemoryRecall(context.Background(), "q", []string{"q"})
	cli.externalMemoryStore(context.Background(), "s", []models.Message{{Role: "user", Content: "hi"}})
	waitCalls(t, f, "memsvc/memory_store", 1)
	leave()
	for _, args := range f.args {
		if args["tenant"] != "slack:u1" {
			t.Fatalf("every external memory call must carry the tenant: %v", args)
		}
	}
}

func TestPendingSegment_RestoresItsWorkspaceOnDrain(t *testing.T) {
	active := &scriptedClient{name: "claude", response: "NOTHING_NEW"}
	mw := newResilienceWorker(t, active)
	mgr := mw.store.Manager()
	recorded := filepath.Join(t.TempDir(), "proj-a")
	mgr.SetWorkspaceDir(recorded)
	path, err := mw.persistPending([]models.Message{{Role: "user", Content: "fato importante"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if len(raw) == 0 {
		t.Fatal("segment written")
	}
	other := filepath.Join(t.TempDir(), "proj-b")
	mgr.SetWorkspaceDir(other)
	if got := mw.drainPending(context.Background()); got != 1 {
		t.Fatalf("drained = %d", got)
	}
	if mgr.WorkspaceDir() != other {
		t.Fatalf("the drainer's workspace must be restored after the segment: %s", mgr.WorkspaceDir())
	}
	restore := mw.enterSegmentWorkspace(recorded)
	if mgr.WorkspaceDir() != recorded {
		t.Fatal("enterSegmentWorkspace must switch")
	}
	restore()
	if mgr.WorkspaceDir() != other {
		t.Fatal("restore must switch back")
	}
}
