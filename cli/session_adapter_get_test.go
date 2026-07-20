/*
 * ChatCLI - @session get adapter tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func newGetTestCLI(t *testing.T) *ChatCLI {
	t.Helper()
	cli := &ChatCLI{sessionManager: newTestSessionManager(t), logger: zap.NewNop()}
	var msgs []models.Message
	for i := 0; i < 30; i++ {
		content := "routine message about builds"
		if i == 25 {
			content = "decision: the oauth refresh token rotates hourly"
		}
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, models.Message{Role: role, Content: content})
	}
	if err := cli.sessionManager.SaveSessionV2("auth-design", &SessionData{Version: 2, ChatHistory: msgs}); err != nil {
		t.Fatal(err)
	}
	return cli
}

func TestSessionAdapterGet_Pagination(t *testing.T) {
	cli := newGetTestCLI(t)
	a := &sessionPluginAdapter{cli: cli}

	out, err := a.Get(context.Background(), "auth-design", 0, 5, "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(out, "[0]") || !strings.Contains(out, "[4]") || strings.Contains(out, "[5]") {
		t.Errorf("first page must span [0..4]: %s", out)
	}
	if !strings.Contains(out, "offset=5") {
		t.Errorf("must hint the next offset: %s", out)
	}

	// Tail page carries no next-offset hint.
	out, err = a.Get(context.Background(), "auth-design", 25, 10, "")
	if err != nil || strings.Contains(out, "offset=3") {
		t.Errorf("tail page must not hint further pages: %v / %s", err, out)
	}
}

func TestSessionAdapterGet_QueryCentersOnBestMatch(t *testing.T) {
	cli := newGetTestCLI(t)
	a := &sessionPluginAdapter{cli: cli}

	out, err := a.Get(context.Background(), "auth-design", 0, 6, "oauth refresh token")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(out, "oauth refresh token rotates hourly") {
		t.Errorf("query must center the page on the matching message: %s", out)
	}
}

func TestSessionAdapterGet_MissingSession(t *testing.T) {
	cli := newGetTestCLI(t)
	a := &sessionPluginAdapter{cli: cli}
	if _, err := a.Get(context.Background(), "nope", 0, 5, ""); err == nil {
		t.Error("missing session must error")
	}
}

// TestSessionAdapterGet_RoleAwareCaps: assistant prose survives past the old
// flat 600-char cap (the "partial recall" failure), while tool dumps stay
// tightly bounded.
func TestSessionAdapterGet_RoleAwareCaps(t *testing.T) {
	cli := &ChatCLI{sessionManager: newTestSessionManager(t), logger: zap.NewNop()}
	long := strings.Repeat("análise detalhada do problema ", 60) // ~1800 chars
	dump := strings.Repeat("tool output line ", 100)             // ~1700 chars
	if err := cli.sessionManager.SaveSessionV2("caps", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "assistant", Content: long},
			{Role: "tool", Content: dump},
		},
	}); err != nil {
		t.Fatal(err)
	}
	a := &sessionPluginAdapter{cli: cli}

	out, err := a.Get(context.Background(), "caps", 0, 10, "")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if strings.Count(out, "análise detalhada") < 50 {
		t.Errorf("assistant prose should keep ~2000 chars, got %d occurrences", strings.Count(out, "análise detalhada"))
	}
	if strings.Count(out, "tool output line") > 40 {
		t.Errorf("tool dump should stay capped at 600 chars, got %d occurrences", strings.Count(out, "tool output line"))
	}
}

// TestSessionAdapterGetMessage: single-message fetch returns the whole entry
// and rejects out-of-range indices with an actionable error.
func TestSessionAdapterGetMessage(t *testing.T) {
	cli := &ChatCLI{sessionManager: newTestSessionManager(t), logger: zap.NewNop()}
	long := strings.Repeat("linha importante da análise. ", 150) // ~4300 chars
	if err := cli.sessionManager.SaveSessionV2("full", &SessionData{
		Version: 2,
		ChatHistory: []models.Message{
			{Role: "user", Content: "pergunta"},
			{Role: "assistant", Content: long},
		},
	}); err != nil {
		t.Fatal(err)
	}
	a := &sessionPluginAdapter{cli: cli}

	out, err := a.GetMessage(context.Background(), "full", 1)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	if len(out) < 4000 {
		t.Errorf("single-message fetch should return the near-full content, got %d bytes", len(out))
	}
	if _, err := a.GetMessage(context.Background(), "full", 99); err == nil {
		t.Error("out-of-range index must error")
	}
}
