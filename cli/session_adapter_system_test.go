/*
 * ChatCLI - @session system-message masking tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

// seedSessionWithSystem saves a session whose history carries a stored system
// prompt — the shape every autosave has, and the exact payload @session get
// used to echo back instead of the conversation.
func seedSessionWithSystem(t *testing.T, sm *SessionManager) {
	t.Helper()
	if err := sm.SaveSessionV2("with-system", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "system", Content: "You are a senior software engineer operating in coder mode. Respond with tool_call blocks and reasoning sections as discussed."},
			{Role: "user", Content: "como resolvo o token expirado do aws-mcp?"},
			{Role: "assistant", Content: "Rode @mcp-login para refazer o OAuth do aws-mcp."},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionGet_MasksSystemMessages(t *testing.T) {
	cli := &ChatCLI{sessionManager: newTestSessionManager(t)}
	seedSessionWithSystem(t, cli.sessionManager)
	a := &sessionPluginAdapter{cli: cli}

	out, err := a.Get(context.Background(), "with-system", 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "senior software engineer") {
		t.Fatalf("stored system prompt leaked into @session get output: %q", out)
	}
	if !strings.Contains(out, sessionSystemOmitted) {
		t.Errorf("expected the omission marker in place of the system prompt, got %q", out)
	}
	if !strings.Contains(out, "token expirado") || !strings.Contains(out, "@mcp-login") {
		t.Errorf("conversation content must remain, got %q", out)
	}
}

func TestSessionGet_QueryNeverCentersOnSystemPrompt(t *testing.T) {
	cli := &ChatCLI{sessionManager: newTestSessionManager(t)}
	seedSessionWithSystem(t, cli.sessionManager)
	a := &sessionPluginAdapter{cli: cli}

	// The failure mode: a generic recap query matched the (huge, generic)
	// system prompt best and the page opened with it. Masked ranking must
	// land on the conversation instead.
	out, err := a.Get(context.Background(), "with-system", 0, 1, "tool_call reasoning discussed")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "senior software engineer") {
		t.Fatalf("query-centered page must never render the system prompt: %q", out)
	}
}

func TestSessionGetMessage_MasksSystemMessage(t *testing.T) {
	cli := &ChatCLI{sessionManager: newTestSessionManager(t)}
	seedSessionWithSystem(t, cli.sessionManager)
	a := &sessionPluginAdapter{cli: cli}

	out, err := a.GetMessage(context.Background(), "with-system", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "senior software engineer") {
		t.Fatalf("GetMessage must mask system prompts too: %q", out)
	}
}

func TestSearchSessions_IgnoresSystemMessages(t *testing.T) {
	sm := newTestSessionManager(t)
	seedSessionWithSystem(t, sm)

	// Terms that appear ONLY in the stored system prompt must not qualify
	// or rank the session.
	hits, err := sm.SearchSessions("senior software engineer", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("system-prompt-only terms must not match any session, got %+v", hits)
	}
	// Conversation terms still match.
	hits, err = sm.SearchSessions("aws-mcp token", 3)
	if err != nil || len(hits) != 1 {
		t.Fatalf("conversation terms must match, got %+v err=%v", hits, err)
	}
}
