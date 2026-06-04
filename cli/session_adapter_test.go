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
