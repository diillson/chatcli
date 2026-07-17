/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestHistoryCap(t *testing.T) {
	t.Setenv("CHATCLI_MCP_MAX_HISTORY", "")
	if got := historyCap(true); got != 0 {
		t.Errorf("full path default must be uncapped (compactor bounds); got %d", got)
	}
	if got := historyCap(false); got != rpcMaxHistory {
		t.Errorf("plain path default must keep the legacy cap; got %d", got)
	}

	t.Setenv("CHATCLI_MCP_MAX_HISTORY", "7")
	if got := historyCap(true); got != 7 {
		t.Errorf("env override must win on the full path; got %d", got)
	}
	if got := historyCap(false); got != 7 {
		t.Errorf("env override must win on the plain path; got %d", got)
	}

	t.Setenv("CHATCLI_MCP_MAX_HISTORY", "not-a-number")
	if got := historyCap(false); got != rpcMaxHistory {
		t.Errorf("invalid env must fall back to the default; got %d", got)
	}
}

func TestCapHistory(t *testing.T) {
	hist := []models.Message{
		{Role: "user", Content: "1"},
		{Role: "assistant", Content: "2"},
		{Role: "user", Content: "3"},
	}
	if got := capHistory(hist, 0); len(got) != 3 {
		t.Errorf("cap 0 = no cap; got %d", len(got))
	}
	got := capHistory(hist, 2)
	if len(got) != 2 || got[0].Content != "2" {
		t.Errorf("cap must keep the newest messages; got %+v", got)
	}
	if got := capHistory(hist, 5); len(got) != 3 {
		t.Errorf("cap above length must be a no-op; got %d", len(got))
	}
}

func TestAutosaveSession(t *testing.T) {
	store := &fakeStore{saved: map[string][]models.Message{}}
	b := &rpcBackend{store: store}
	hist := []models.Message{
		{Role: "user", Content: "keep me"},
		{Role: "assistant", Content: "kept"},
	}

	// Default ON: with neither env set, MCP conversations persist — the same
	// default the interactive REPL autosave uses.
	t.Setenv("CHATCLI_MCP_SESSION_AUTOSAVE", "")
	t.Setenv("CHATCLI_SESSION_AUTOSAVE", "")
	b.autosaveSession("sess", hist)
	if got := store.saved["mcp-sess"]; len(got) != 2 || got[0].Content != "keep me" {
		t.Fatalf("autosave must be ON by default and persist under mcp-<session>; got %+v", store.saved)
	}

	// The global session-autosave gate disables MCP autosave too…
	delete(store.saved, "mcp-sess")
	t.Setenv("CHATCLI_SESSION_AUTOSAVE", "false")
	b.autosaveSession("sess", hist)
	if len(store.saved) != 0 {
		t.Fatal("global CHATCLI_SESSION_AUTOSAVE=false must disable MCP autosave")
	}

	// …but an explicit MCP setting always wins, in both directions.
	t.Setenv("CHATCLI_MCP_SESSION_AUTOSAVE", "true")
	b.autosaveSession("sess", hist)
	if len(store.saved["mcp-sess"]) != 2 {
		t.Fatal("explicit CHATCLI_MCP_SESSION_AUTOSAVE=true must override the global gate")
	}
	delete(store.saved, "mcp-sess")
	t.Setenv("CHATCLI_SESSION_AUTOSAVE", "")
	t.Setenv("CHATCLI_MCP_SESSION_AUTOSAVE", "false")
	b.autosaveSession("sess", hist)
	if len(store.saved) != 0 {
		t.Fatal("explicit CHATCLI_MCP_SESSION_AUTOSAVE=false must win over the default")
	}

	// Trivial histories (fewer than 2 non-system messages) are skipped.
	t.Setenv("CHATCLI_MCP_SESSION_AUTOSAVE", "true")
	b.autosaveSession("sess", []models.Message{{Role: "user", Content: "solo"}})
	if len(store.saved) != 0 {
		t.Fatal("trivial history must not autosave")
	}

	b.autosaveSession("sess", nil)
	nilStore := &rpcBackend{}
	nilStore.autosaveSession("sess", hist) // no store: must not panic
}

// TestSessionSearchAndResources_DegradedGuards covers the capability methods
// when ChatCLI failed to initialize: actionable errors, never a panic.
func TestSessionSearchAndResources_DegradedGuards(t *testing.T) {
	b := &rpcBackend{} // cli == nil

	if _, err := b.SearchSessions(context.Background(), "q"); err == nil {
		t.Error("SearchSessions must fail without a ChatCLI")
	}
	if _, err := b.ForkSession(context.Background(), "a", "b"); err == nil {
		t.Error("ForkSession must fail without a ChatCLI")
	}
	if got := b.Resources(); got != nil {
		t.Errorf("Resources must be empty without a ChatCLI; got %v", got)
	}
	if _, err := b.ReadResource(context.Background(), "chatcli://memory/index"); err == nil {
		t.Error("ReadResource must fail without a ChatCLI")
	}
}
