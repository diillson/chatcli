/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

// Binding lifecycle through the per-session /session command surface: attach
// to a not-yet-existing session binds without loading; a completed turn's
// write-through then creates the file; detach stops the write-through.
func TestRunSessionCommand_AttachWriteThroughDetach(t *testing.T) {
	store := newFakeStore()
	b := sessionBackend(store)
	ctx := context.Background()

	out, err := b.runSessionCommand(ctx, "acp-1", []string{"attach", "shared"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if !strings.Contains(out, "shared") {
		t.Fatalf("attach reply should name the session, got %q", out)
	}
	if got := b.boundName("acp-1"); got != "shared" {
		t.Fatalf("expected binding to shared, got %q", got)
	}

	// A turn completes: the write-through must materialize the file.
	hist := sessionMsgs(4)
	b.mu.Lock()
	b.sessions["acp-1"] = hist
	b.mu.Unlock()
	b.writeThrough("acp-1", hist)
	if len(store.saved["shared"]) != 4 {
		t.Fatalf("write-through should persist 4 messages, got %d", len(store.saved["shared"]))
	}

	if _, err := b.runSessionCommand(ctx, "acp-1", []string{"detach"}); err != nil {
		t.Fatalf("detach: %v", err)
	}
	b.mu.Lock()
	b.sessions["acp-1"] = sessionMsgs(6)
	b.mu.Unlock()
	b.writeThrough("acp-1", sessionMsgs(6))
	if len(store.saved["shared"]) != 4 {
		t.Fatalf("after detach the store must keep the last bound snapshot (4), got %d", len(store.saved["shared"]))
	}
}

// refreshBound adopts a write made by another surface (newer store mtime)
// and ignores stale files (mtime not after the sync stamp).
func TestRefreshBound_AdoptsNewerExternalWrite(t *testing.T) {
	store := newFakeStore()
	b := sessionBackend(store)
	ctx := context.Background()

	store.saved["shared"] = sessionMsgs(2)
	store.mtimes["shared"] = time.Now()
	if _, err := b.runSessionCommand(ctx, "mcp", []string{"load", "shared"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	b.mu.Lock()
	got := len(b.sessions["mcp"])
	b.mu.Unlock()
	if got != 2 {
		t.Fatalf("load should hydrate 2 messages, got %d", got)
	}

	// No external write: refresh must be a no-op.
	b.refreshBound("mcp")
	b.mu.Lock()
	b.sessions["mcp"] = append(b.sessions["mcp"], models.Message{Role: "user", Content: "local"})
	b.mu.Unlock()

	// Another surface writes the file (newer mtime, more content).
	store.saved["shared"] = sessionMsgs(5)
	store.mtimes["shared"] = time.Now().Add(time.Second)
	b.refreshBound("mcp")
	b.mu.Lock()
	got = len(b.sessions["mcp"])
	b.mu.Unlock()
	if got != 5 {
		t.Fatalf("refresh should adopt the external write (5 messages), got %d", got)
	}
}

// Loading a missing session errors; attach is the create path. Deleting a
// bound name must also drop the binding (no silent resurrection).
func TestRunSessionCommand_LoadMissingAndDeleteUnbinds(t *testing.T) {
	store := newFakeStore()
	b := sessionBackend(store)
	ctx := context.Background()

	if _, err := b.runSessionCommand(ctx, "s1", []string{"load", "ghost"}); err == nil {
		t.Fatal("load of a missing session must error")
	}

	store.saved["work"] = sessionMsgs(2)
	store.mtimes["work"] = time.Now()
	if _, err := b.runSessionCommand(ctx, "s1", []string{"attach", "work"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := b.runSessionCommand(ctx, "s1", []string{"delete", "work"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := b.boundName("s1"); got != "" {
		t.Fatalf("delete of the bound name must unbind, still bound to %q", got)
	}
	b.writeThrough("s1", sessionMsgs(3))
	if _, ok := store.saved["work"]; ok {
		t.Fatal("write-through after delete must not resurrect the session")
	}
}

// ManageSession gained attach/detach/status; save and load now bind.
func TestManageSession_AttachDetachStatus(t *testing.T) {
	store := newFakeStore()
	b := sessionBackend(store)
	ctx := context.Background()

	b.mu.Lock()
	b.sessions["mcp"] = sessionMsgs(3)
	b.mu.Unlock()
	if _, err := b.ManageSession(ctx, "save", "mcp", "notes"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := b.boundName("mcp"); got != "notes" {
		t.Fatalf("save must bind, got %q", got)
	}

	out, err := b.ManageSession(ctx, "status", "mcp", "")
	if err != nil || !strings.Contains(out, "notes") {
		t.Fatalf("status should report the binding, got %q err=%v", out, err)
	}

	if _, err := b.ManageSession(ctx, "detach", "mcp", ""); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if got := b.boundName("mcp"); got != "" {
		t.Fatalf("detach must unbind, got %q", got)
	}

	if _, err := b.ManageSession(ctx, "attach", "mcp", "notes"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	b.mu.Lock()
	got := len(b.sessions["mcp"])
	b.mu.Unlock()
	if got != 3 {
		t.Fatalf("attach to an existing session must hydrate it (3 messages), got %d", got)
	}

	// clear must drop the binding too.
	if _, err := b.ManageSession(ctx, "clear", "mcp", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := b.boundName("mcp"); got != "" {
		t.Fatalf("clear must unbind, got %q", got)
	}
}

// RestoreSession (ACP session/load): live state wins, the mcp- autosave
// mirror is the restart path, and system/empty messages are filtered out.
func TestRestoreSession_LiveThenMirror(t *testing.T) {
	store := newFakeStore()
	b := sessionBackend(store)
	ctx := context.Background()

	// Restart scenario: nothing live, only the rolling mirror.
	store.saved["mcp-old-session"] = []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	items, err := b.RestoreSession(ctx, "old-session")
	if err != nil {
		t.Fatalf("restore from mirror: %v", err)
	}
	if len(items) != 2 || items[0].Role != "user" || items[1].Role != "assistant" {
		t.Fatalf("unexpected replay items: %+v", items)
	}
	// The restore must hydrate the live session for the next prompt.
	b.mu.Lock()
	live := len(b.sessions["old-session"])
	b.mu.Unlock()
	if live != 3 {
		t.Fatalf("restore should hydrate the live session (3 raw messages), got %d", live)
	}

	if _, err := b.RestoreSession(ctx, "never-existed"); err == nil {
		t.Fatal("restoring an unknown session must error")
	}
}
