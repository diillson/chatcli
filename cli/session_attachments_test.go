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
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func newAttachTestCLI(t *testing.T) (*ChatCLI, *ctxmgr.Manager, string) {
	t.Helper()
	mgr, err := ctxmgr.NewManagerWithBasePath(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	src := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(src, []byte("# Notes\nDeploy uses helm.\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fc, err := mgr.CreateContext(context.Background(), "notes", "test", []string{src}, ctxmgr.ModeFull, nil, false)
	if err != nil {
		t.Fatalf("create context: %v", err)
	}
	cli := &ChatCLI{logger: zap.NewNop(), contextHandler: &ContextHandler{manager: mgr, logger: zap.NewNop()}}
	return cli, mgr, fc.ID
}

func TestSessionAttachments_RoundTrip(t *testing.T) {
	cli, mgr, id := newAttachTestCLI(t)
	cli.currentSessionName = "work"
	if err := mgr.AttachContextWithOptions("work", id, ctxmgr.AttachOptions{Priority: 2, RetrievalTopK: 6}); err != nil {
		t.Fatalf("attach: %v", err)
	}

	sd := cli.buildSessionData()
	if len(sd.Attachments) != 1 || sd.Attachments[0].ContextID != id || sd.Attachments[0].RetrievalTopK != 6 || sd.Attachments[0].Priority != 2 {
		t.Fatalf("attachments not captured: %+v", sd.Attachments)
	}

	// A fresh process (same store, nothing attached) loads the session.
	fresh := &ChatCLI{logger: zap.NewNop(), contextHandler: &ContextHandler{manager: mgr, logger: zap.NewNop()}}
	fresh.applySessionAttachments(sd, "resumed")
	got := mgr.AttachedRecords("resumed")
	if len(got) != 1 || got[0].ContextID != id || got[0].RetrievalTopK != 6 {
		t.Fatalf("attachments not restored: %+v", got)
	}
	// Idempotent: applying again does not duplicate.
	fresh.applySessionAttachments(sd, "resumed")
	if n := len(mgr.AttachedRecords("resumed")); n != 1 {
		t.Fatalf("re-apply duplicated attachments: %d", n)
	}
	// A stale record (deleted context) is skipped without failing.
	stale := &models.SessionData{Attachments: []models.SessionAttachment{{ContextID: "ctx-gone"}}}
	fresh.applySessionAttachments(stale, "resumed")
	if n := len(mgr.AttachedRecords("resumed")); n != 1 {
		t.Fatalf("stale record must be skipped: %d", n)
	}
	// No handler → nil-safe.
	(&ChatCLI{}).applySessionAttachments(sd, "x")
	if (&ChatCLI{}).sessionAttachments() != nil {
		t.Fatal("no handler → no attachments")
	}
}

// Auto-recall must ignore facts that match only an incidental token and
// must not reinforce facts merely for being pushed.
func TestMemoryAutoRecall_FloorAndNoReinforcement(t *testing.T) {
	t.Setenv("CHATCLI_MEMORY_AUTORECALL", "")
	store := workspace.NewMemoryStore(t.TempDir(), zap.NewNop())
	cli := &ChatCLI{memoryStore: store, logger: zap.NewNop()}
	mgr := store.Manager()
	if mgr == nil || mgr.Facts == nil {
		t.Skip("memory manager unavailable in this build")
	}
	if !mgr.Facts.AddFact("The deploy pipeline uses helm charts from the infra repo", "project", []string{"deploy", "helm"}) {
		t.Fatal("add fact")
	}

	before := map[string]int{}
	for _, f := range mgr.Facts.GetAll() {
		before[f.ID] = f.AccessCount
	}
	strong := cli.memoryAutoRecallBlockCtx(context.Background(), []string{"deploy", "helm", "pipeline"}, "how does the deploy pipeline work")
	if !strings.Contains(strong, "helm charts") {
		t.Fatalf("strong match must be recalled, got %q", strong)
	}
	weak := cli.memoryAutoRecallBlockCtx(context.Background(), []string{"coffee", "office", "floor", "broken", "repo"}, "the coffee machine on the office floor is broken")
	if weak != "" {
		t.Fatalf("one incidental token must not recall the fact, got %q", weak)
	}
	for _, f := range mgr.Facts.GetAll() {
		if f.AccessCount != before[f.ID] {
			t.Fatalf("auto-recall injection must not reinforce access (before=%d after=%d)", before[f.ID], f.AccessCount)
		}
	}
	if cli.memoryAutoRecallBlock(nil) != "" {
		t.Fatal("no hints → no block")
	}
}
