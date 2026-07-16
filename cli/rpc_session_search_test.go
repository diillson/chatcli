/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

// i18n is initialized by TestMain in config_sections_test.go.

func newSessionRPCCLI(t *testing.T) *ChatCLI {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	sm, err := NewSessionManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	return &ChatCLI{logger: zap.NewNop(), sessionManager: sm}
}

func TestSearchSessionsRPC(t *testing.T) {
	c := newSessionRPCCLI(t)
	if err := c.sessionManager.SaveSessionV2("incident-kafka", &SessionData{
		ChatHistory: []models.Message{
			{Role: "user", Content: "the kafka consumer lag exploded"},
			{Role: "assistant", Content: "check partition rebalance"},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := c.SearchSessionsRPC("kafka lag")
	if err != nil {
		t.Fatalf("SearchSessionsRPC: %v", err)
	}
	if !strings.Contains(out, "incident-kafka") || !strings.Contains(out, "matches") {
		t.Errorf("hit rendering missing session/count:\n%s", out)
	}

	out, err = c.SearchSessionsRPC("nothing-matches-this")
	if err != nil {
		t.Fatalf("no-hit search must not error: %v", err)
	}
	if !strings.Contains(out, "no saved session") {
		t.Errorf("no-hit message wrong: %q", out)
	}

	if _, err := (&ChatCLI{logger: zap.NewNop()}).SearchSessionsRPC("x"); err == nil {
		t.Error("nil sessionManager must error")
	}
}

func TestForkSessionRPC(t *testing.T) {
	c := newSessionRPCCLI(t)
	if err := c.sessionManager.SaveSessionV2("origin-sess", &SessionData{
		ChatHistory: []models.Message{{Role: "user", Content: "seed"}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := c.ForkSessionRPC("origin-sess", "copy-sess"); err != nil {
		t.Fatalf("ForkSessionRPC: %v", err)
	}
	forked, err := c.sessionManager.LoadSessionV2("copy-sess")
	if err != nil || len(forked.ChatHistory) != 1 {
		t.Fatalf("forked copy unreadable: %+v, %v", forked, err)
	}

	// Both names are validated — this surface is remote-reachable.
	if err := c.ForkSessionRPC("../evil", "ok-name"); err == nil {
		t.Error("invalid source name must be refused")
	}
	if err := c.ForkSessionRPC("origin-sess", "../evil"); err == nil {
		t.Error("invalid target name must be refused")
	}
	if err := (&ChatCLI{logger: zap.NewNop()}).ForkSessionRPC("a", "b"); err == nil {
		t.Error("nil sessionManager must error")
	}
}

// TestStartHubResume_WithLiveSyncAndDegradedPaths covers the ChatCLI-level
// resume wrapper: with a live hubSync it adopts the active conversation; a
// remote session (isRemote) leaves everything untouched.
func TestStartHubResume_WithLiveSyncAndDegradedPaths(t *testing.T) {
	store, err := hub.OpenSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open hub store: %v", err)
	}
	defer func() { _ = store.Close() }()
	c := &ChatCLI{logger: zap.NewNop()}
	c.EnableHubSync(newLocalHubClient(store, "resume-user"))

	if closeFn := c.StartHubResume(t.Context(), ""); closeFn != nil {
		defer closeFn()
	}
	if c.hubSync == nil {
		t.Fatal("live hubSync must survive a successful resume")
	}
	if conv, _ := c.hubSync.status(); conv == "" {
		t.Fatal("resume must adopt (or lazily create) the active conversation")
	}

	remote := &ChatCLI{logger: zap.NewNop(), isRemote: true}
	if closeFn := remote.StartHubResume(t.Context(), "iso"); closeFn != nil {
		t.Error("remote session must not open a local hub store")
	}
	if remote.hubSync != nil {
		t.Error("remote session must stay without hubSync")
	}
}
