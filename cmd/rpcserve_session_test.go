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

// Walks the remaining /session subcommand surface end to end: usage, save
// (validation, empty, success+bind), status, fork (memory and bound source),
// new, list, search validation and the unknown fallback.
func TestRunSessionCommand_FullSurface(t *testing.T) {
	store := newFakeStore()
	b := sessionBackend(store)
	ctx := context.Background()

	// i18n may or may not be initialized in this package's test binary
	// (initializing it here would poison initOnce for other packages), so
	// accept either the raw key or the resolved English line.
	out, err := b.runSessionCommand(ctx, "s1", nil)
	if err != nil || (!strings.Contains(out, "session.usage_save") && !strings.Contains(out, "/session save")) {
		t.Fatalf("empty args must print usage, got %q err=%v", out, err)
	}
	if out, _ := b.runSessionCommand(ctx, "s1", []string{"save"}); !strings.Contains(out, strings.TrimSpace(out)) || out == "" {
		t.Fatal("save without name must answer with the validation message")
	}
	if out, _ := b.runSessionCommand(ctx, "s1", []string{"save", "empty"}); out == "" {
		t.Fatal("save with no live messages must answer, not error")
	}
	if _, ok := store.saved["empty"]; ok {
		t.Fatal("empty save must not create a file")
	}

	b.mu.Lock()
	b.sessions["s1"] = sessionMsgs(4)
	b.mu.Unlock()
	if _, err := b.runSessionCommand(ctx, "s1", []string{"save", "work"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(store.saved["work"]) != 4 || b.boundName("s1") != "work" {
		t.Fatal("save must persist 4 messages and bind")
	}

	if out, _ := b.runSessionCommand(ctx, "s1", []string{"status"}); !strings.Contains(out, "work") {
		t.Fatalf("status must report the binding, got %q", out)
	}

	// fork from a bound source needs the full ChatCLI (ForkSessionRPC);
	// this backend runs degraded (cli == nil), so the error is the contract.
	if _, err := b.runSessionCommand(ctx, "s1", []string{"fork", "work-v2"}); err == nil {
		t.Fatal("bound-source fork without ChatCLI must surface the degraded-mode error")
	}

	if out, _ := b.runSessionCommand(ctx, "s1", []string{"list"}); !strings.Contains(out, "work") {
		t.Fatalf("list must include saved sessions, got %q", out)
	}
	if out, _ := b.runSessionCommand(ctx, "s1", []string{"search"}); out == "" {
		t.Fatal("search without query must answer with usage")
	}
	if out, _ := b.runSessionCommand(ctx, "s1", []string{"wat"}); out == "" {
		t.Fatal("unknown subcommand must answer, not error")
	}

	// new clears the live session, unbinds and keeps the store intact.
	if _, err := b.runSessionCommand(ctx, "s1", []string{"new"}); err != nil {
		t.Fatalf("new: %v", err)
	}
	if b.boundName("s1") != "" {
		t.Fatal("new must unbind")
	}
	b.mu.Lock()
	live := len(b.sessions["s1"])
	b.mu.Unlock()
	if live != 0 {
		t.Fatalf("new must clear the live session, got %d", live)
	}

	// fork with no bound source snapshots the in-memory conversation.
	b.mu.Lock()
	b.sessions["s2"] = sessionMsgs(2)
	b.mu.Unlock()
	if _, err := b.runSessionCommand(ctx, "s2", []string{"fork", "branch"}); err != nil {
		t.Fatalf("fork memory: %v", err)
	}
	if len(store.saved["branch"]) != 2 || b.boundName("s2") != "branch" {
		t.Fatal("memory fork must snapshot and bind")
	}
}

// ManageSession attach to a missing name must keep the live conversation as
// the seed (dropping it would destroy the caller's in-flight context), and
// the follow-up write-through creates the file from it.
func TestManageSession_AttachMissingKeepsLiveSeed(t *testing.T) {
	store := newFakeStore()
	b := sessionBackend(store)
	ctx := context.Background()

	b.mu.Lock()
	b.sessions["mcp"] = sessionMsgs(5)
	b.mu.Unlock()
	if _, err := b.ManageSession(ctx, "attach", "mcp", "fresh"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	b.mu.Lock()
	live := len(b.sessions["mcp"])
	b.mu.Unlock()
	if live != 5 {
		t.Fatalf("attach to a missing name must keep the live seed (5), got %d", live)
	}
	b.writeThrough("mcp", sessionMsgs(5))
	if len(store.saved["fresh"]) != 5 {
		t.Fatalf("first write-through must create the file from the seed, got %d", len(store.saved["fresh"]))
	}

	// detach with no binding answers, never errors.
	if _, err := b.ManageSession(ctx, "detach", "ghost-session", ""); err != nil {
		t.Fatalf("detach unbound: %v", err)
	}
	// status for an unbound session names the live count.
	if out, _ := b.ManageSession(ctx, "status", "ghost-session", ""); !strings.Contains(out, "not bound") {
		t.Fatalf("status unbound: %q", out)
	}
}

// trimDanglingToolPairs drops orphaned halves of tool exchanges at the
// history's edges and leaves interior pairs alone.
func TestTrimDanglingToolPairs(t *testing.T) {
	hist := []models.Message{
		{Role: "tool", Content: "orphan result"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "call", ToolCalls: []models.ToolCall{{}}},
		{Role: "tool", Content: "result"},
		{Role: "assistant", Content: "answer"},
		{Role: "assistant", Content: "pending", ToolCalls: []models.ToolCall{{}}},
	}
	got := trimDanglingToolPairs(hist)
	if len(got) != 4 {
		t.Fatalf("expected 4 messages after trimming both edges, got %d", len(got))
	}
	if got[0].Role != "user" || got[len(got)-1].Content != "answer" {
		t.Fatalf("interior pair must survive intact: %+v", got)
	}
	// capHistory applies the same trim after the numeric cut: the last 3
	// messages are [tool-result, answer, pending-call]; the pending call is
	// dropped, and the now-leading tool result lost its assistant call to
	// the cut, so only the prose answer survives.
	if capped := capHistory(hist, 3); len(capped) != 1 || capped[0].Content != "answer" {
		t.Fatalf("capped history must trim the dangling edges, got %+v", capped)
	}
}
