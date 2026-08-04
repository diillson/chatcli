/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestSessionAdapter_Search(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("alpha", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "How do I design a rate limiter?"},
			{Role: "assistant", Content: "Use a token bucket."},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sm.SaveSessionV2("beta", &SessionData{
		Version:     2,
		ChatHistory: []models.Message{{Role: "user", Content: "Unrelated topic."}},
	}); err != nil {
		t.Fatal(err)
	}

	a := &sessionPluginAdapter{cli: &ChatCLI{sessionManager: sm, logger: zap.NewNop()}}

	out, err := a.Search(context.Background(), "rate limiter", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("expected alpha in results, got %q", out)
	}
	if strings.Contains(out, "beta") {
		t.Fatalf("beta should not match, got %q", out)
	}
}

func TestSessionAdapter_SearchNoMatch(t *testing.T) {
	sm := newTestSessionManager(t)
	a := &sessionPluginAdapter{cli: &ChatCLI{sessionManager: sm, logger: zap.NewNop()}}
	out, err := a.Search(context.Background(), "nothing here", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "no saved session") && !strings.Contains(out, "Nenhuma") {
		t.Fatalf("expected no-match message, got %q", out)
	}
}

func TestSessionAdapter_SaveCreatesFileAndBinds(t *testing.T) {
	sm := newTestSessionManager(t)
	c := &ChatCLI{sessionManager: sm, logger: zap.NewNop()}
	c.history = []models.Message{{Role: "user", Content: "hello"}}
	a := &sessionPluginAdapter{cli: c}

	out, err := a.Save(context.Background(), "phase3")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if out == "" {
		t.Fatal("Save must return a confirmation for the model")
	}
	if !sm.SessionExists("phase3") {
		t.Fatal("Save must create the session file")
	}
	if c.currentSessionName != "phase3" {
		t.Fatalf("Save must bind, got %q", c.currentSessionName)
	}
	if c.boundSessionSync.IsZero() {
		t.Fatal("Save must stamp the write-through sync watermark")
	}
	sd, err := sm.LoadSessionV2("phase3")
	if err != nil || len(sd.ChatHistory) != 1 || sd.ChatHistory[0].Content != "hello" {
		t.Fatalf("saved content mismatch: %+v err=%v", sd, err)
	}
}

func TestSessionAdapter_SaveRejectsMachineNames(t *testing.T) {
	sm := newTestSessionManager(t)
	a := &sessionPluginAdapter{cli: &ChatCLI{sessionManager: sm, logger: zap.NewNop()}}
	if _, err := a.Save(context.Background(), "autosave-20260101-0000"); err == nil {
		t.Fatal("machine-prefixed names must be rejected (they can never be live bindings)")
	}
}

func TestSessionAdapter_ForkCopiesAndBinds(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("src", &SessionData{
		Version:     2,
		ChatHistory: []models.Message{{Role: "user", Content: "origin"}},
	}); err != nil {
		t.Fatal(err)
	}
	c := &ChatCLI{sessionManager: sm, logger: zap.NewNop(), currentSessionName: "src"}
	a := &sessionPluginAdapter{cli: c}

	if _, err := a.Fork(context.Background(), "dst"); err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if !sm.SessionExists("src") || !sm.SessionExists("dst") {
		t.Fatal("fork must copy, keeping the source")
	}
	sd, err := sm.LoadSessionV2("dst")
	if err != nil || len(sd.ChatHistory) != 1 || sd.ChatHistory[0].Content != "origin" {
		t.Fatalf("forked content mismatch: %+v err=%v", sd, err)
	}
	if c.currentSessionName != "dst" {
		t.Fatalf("fork must bind to the fork, got %q", c.currentSessionName)
	}
}

func TestSessionAdapter_ForkFromUnsavedHistory(t *testing.T) {
	sm := newTestSessionManager(t)
	c := &ChatCLI{sessionManager: sm, logger: zap.NewNop()}
	c.history = []models.Message{{Role: "user", Content: "unsaved work"}}
	a := &sessionPluginAdapter{cli: c}

	if _, err := a.Fork(context.Background(), "branch"); err != nil {
		t.Fatalf("Fork: %v", err)
	}
	sd, err := sm.LoadSessionV2("branch")
	if err != nil || len(sd.ChatHistory) != 1 {
		t.Fatalf("in-memory fork mismatch: %+v err=%v", sd, err)
	}
	if c.currentSessionName != "branch" {
		t.Fatalf("bound to %q", c.currentSessionName)
	}
}

func TestSessionAdapter_AttachMissingBindsWithoutLoading(t *testing.T) {
	sm := newTestSessionManager(t)
	c := &ChatCLI{sessionManager: sm, logger: zap.NewNop()}
	c.history = []models.Message{{Role: "user", Content: "in flight"}}
	a := &sessionPluginAdapter{cli: c}

	out, err := a.Attach(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if out == "" {
		t.Fatal("Attach must return a confirmation for the model")
	}
	if c.currentSessionName != "ghost" {
		t.Fatalf("Attach must bind, got %q", c.currentSessionName)
	}
	// Lazy creation: no file yet, and the running conversation is untouched.
	if sm.SessionExists("ghost") {
		t.Fatal("attach to a missing session must not create the file eagerly")
	}
	if len(c.history) != 1 || c.history[0].Content != "in flight" {
		t.Fatal("attach must not touch the in-memory history")
	}
}

func TestSessionAdapter_AttachExistingLoadsOnlyWhenHistoryEmpty(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("prior", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "old question"},
			{Role: "assistant", Content: "old answer"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Empty conversation → the saved history is loaded immediately.
	empty := &ChatCLI{sessionManager: sm, logger: zap.NewNop()}
	if _, err := (&sessionPluginAdapter{cli: empty}).Attach(context.Background(), "prior"); err != nil {
		t.Fatalf("Attach(empty): %v", err)
	}
	if len(empty.history) != 2 || empty.currentSessionName != "prior" {
		t.Fatalf("empty-history attach must load: len=%d name=%q", len(empty.history), empty.currentSessionName)
	}

	// Mid-turn conversation → MERGE: the target's history is prepended so
	// the end-of-turn write-through persists the union. Bind-only here
	// would let that write-through overwrite the target file with just the
	// current conversation — destroying the attached session's content.
	busy := &ChatCLI{sessionManager: sm, logger: zap.NewNop()}
	busy.history = []models.Message{{Role: "user", Content: "current work"}}
	if _, err := (&sessionPluginAdapter{cli: busy}).Attach(context.Background(), "prior"); err != nil {
		t.Fatalf("Attach(busy): %v", err)
	}
	if busy.currentSessionName != "prior" {
		t.Fatalf("busy attach must still bind, got %q", busy.currentSessionName)
	}
	if len(busy.history) != 3 {
		t.Fatalf("busy attach must merge target(2)+current(1), got %d", len(busy.history))
	}
	if busy.history[len(busy.history)-1].Content != "current work" {
		t.Fatal("busy attach must keep the in-flight conversation as the tail")
	}
	if busy.boundSessionSync.IsZero() {
		t.Fatal("busy attach must stamp the sync watermark")
	}
}

func TestSessionAdapter_DetachClearsBinding(t *testing.T) {
	sm := newTestSessionManager(t)
	c := &ChatCLI{sessionManager: sm, logger: zap.NewNop(), currentSessionName: "bound"}
	a := &sessionPluginAdapter{cli: c}

	if _, err := a.Detach(context.Background()); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if c.currentSessionName != "" {
		t.Fatalf("Detach must clear the binding, got %q", c.currentSessionName)
	}
	// Detaching twice is a no-op, not an error.
	if _, err := a.Detach(context.Background()); err != nil {
		t.Fatalf("second Detach: %v", err)
	}
}

func TestSessionAdapter_List(t *testing.T) {
	sm := newTestSessionManager(t)
	if err := sm.SaveSessionV2("proj-x", &SessionData{Version: 2, ChatHistory: []models.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	a := &sessionPluginAdapter{cli: &ChatCLI{sessionManager: sm, logger: zap.NewNop()}}
	out, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(out, "proj-x") {
		t.Fatalf("expected proj-x in list, got %q", out)
	}
}
