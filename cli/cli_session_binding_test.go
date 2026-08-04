/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func bindingTestCLI(t *testing.T) *ChatCLI {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	sm, err := NewSessionManager(zap.NewNop())
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}
	return &ChatCLI{sessionManager: sm, logger: zap.NewNop()}
}

// A named session persists every completed turn and adopts writes made by
// another surface — the whole cross-surface loop, against the real store.
func TestBoundSession_WriteThroughAndRefresh(t *testing.T) {
	c := bindingTestCLI(t)
	c.currentSessionName = "shared"
	c.history = []models.Message{
		{Role: "user", Content: "oi"},
		{Role: "assistant", Content: "olá"},
	}

	c.persistBoundSession()
	if !c.sessionManager.SessionExists("shared") {
		t.Fatal("write-through must create the session file")
	}

	// No external write: refresh keeps local state.
	c.refreshBoundSession()
	if len(c.history) != 2 {
		t.Fatalf("refresh without external write must be a no-op, got %d messages", len(c.history))
	}

	// Another surface writes the file (newer mtime).
	external := &SessionData{Version: 2, ChatHistory: []models.Message{
		{Role: "user", Content: "oi"},
		{Role: "assistant", Content: "olá"},
		{Role: "user", Content: "vindo do ACP"},
		{Role: "assistant", Content: "resposta do ACP"},
	}}
	if err := c.sessionManager.SaveSessionV2("shared", external); err != nil {
		t.Fatalf("external save: %v", err)
	}
	// Filesystem mtime granularity can hide a same-instant write; force it.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(c.sessionManager.getSessionPath("shared"), future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	c.refreshBoundSession()
	if len(c.history) != 4 {
		t.Fatalf("refresh must adopt the external write (4 messages), got %d", len(c.history))
	}
}

// Machine-prefixed names and captured RPC runs must never write through.
func TestBoundSession_Guards(t *testing.T) {
	c := bindingTestCLI(t)
	c.history = []models.Message{{Role: "user", Content: "x"}, {Role: "assistant", Content: "y"}}

	// Autosave mirrors are owned by the autosave paths.
	c.currentSessionName = "autosave-20260801-1200"
	c.persistBoundSession()
	if c.sessionManager.SessionExists("autosave-20260801-1200") {
		t.Fatal("machine-prefixed names must not write through")
	}

	// During a captured RPC run currentSessionName holds a surface session
	// id — the REPL hooks must stand down.
	c.currentSessionName = "acp-uuid-123"
	rpcCaptureActive.Store(true)
	defer rpcCaptureActive.Store(false)
	c.persistBoundSession()
	if c.sessionManager.SessionExists("acp-uuid-123") {
		t.Fatal("captured RPC runs must not trigger the REPL write-through")
	}
}

// CHATCLI_SESSION_WRITETHROUGH=off disables both directions.
func TestBoundSession_EnvKillSwitch(t *testing.T) {
	c := bindingTestCLI(t)
	t.Setenv("CHATCLI_SESSION_WRITETHROUGH", "off")
	c.currentSessionName = "shared"
	c.history = []models.Message{{Role: "user", Content: "x"}, {Role: "assistant", Content: "y"}}
	c.persistBoundSession()
	if c.sessionManager.SessionExists("shared") {
		t.Fatal("write-through must respect the kill switch")
	}
}
