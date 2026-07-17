/*
 * ChatCLI - Machine-session lifecycle tests (prune + TTL).
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

func seedSession(t *testing.T, sm *SessionManager, name string, age time.Duration) {
	t.Helper()
	if err := sm.SaveSessionV2(name, &SessionData{Version: 2, ChatHistory: []models.Message{
		{Role: "user", Content: "hello from " + name},
	}}); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		path := filepath.Join(sm.sessionsDir, name+".json")
		old := time.Now().Add(-age)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPruneSessionsByPrefix_KeepsNewestByMtime(t *testing.T) {
	sm := newTestSessionManager(t)
	seedSession(t, sm, "mcp-old", 72*time.Hour)
	seedSession(t, sm, "mcp-mid", 48*time.Hour)
	seedSession(t, sm, "mcp-new", 0)
	seedSession(t, sm, "user-named", 100*24*time.Hour) // different prefix: untouched

	if removed := sm.PruneSessionsByPrefix("mcp-", 2); removed != 1 {
		t.Fatalf("expected 1 pruned, got %d", removed)
	}
	names, _ := sm.ListSessions()
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if got["mcp-old"] {
		t.Error("oldest mcp- session must be pruned")
	}
	for _, want := range []string{"mcp-mid", "mcp-new", "user-named"} {
		if !got[want] {
			t.Errorf("session %q must survive, have %v", want, names)
		}
	}
}

func TestCleanExpiredMachineSessions_SparesUserNamed(t *testing.T) {
	sm := newTestSessionManager(t)
	seedSession(t, sm, "autosave-20260101-120000", 200*24*time.Hour)
	seedSession(t, sm, "mcp-stale", 200*24*time.Hour)
	seedSession(t, sm, "important-checkpoint", 200*24*time.Hour) // user-named, ancient
	seedSession(t, sm, "autosave-20260715-120000", 0)            // fresh machine session

	if cleaned := sm.CleanExpiredMachineSessions(); cleaned != 2 {
		t.Fatalf("expected 2 expired machine sessions cleaned, got %d", cleaned)
	}
	names, _ := sm.ListSessions()
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["important-checkpoint"] {
		t.Error("user-named sessions must NEVER be expired automatically")
	}
	if !got["autosave-20260715-120000"] {
		t.Error("fresh machine session must survive")
	}
	if got["mcp-stale"] || got["autosave-20260101-120000"] {
		t.Error("stale machine sessions must be expired")
	}
}

func TestCleanExpiredMachineSessions_ZeroTTLDisables(t *testing.T) {
	sm := newTestSessionManager(t)
	seedSession(t, sm, "mcp-ancient", 400*24*time.Hour)

	t.Setenv("CHATCLI_SESSION_TTL", "0")
	if cleaned := sm.CleanExpiredMachineSessions(); cleaned != 0 {
		t.Fatalf("TTL=0 must disable expiry entirely, cleaned %d", cleaned)
	}
	names, _ := sm.ListSessions()
	if len(names) != 1 {
		t.Errorf("ancient machine session must survive with TTL=0, have %v", names)
	}
}
