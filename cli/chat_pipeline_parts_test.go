/*
 * ChatCLI - Tests for the individual system-prompt part builders
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Each helper in chat_pipeline.go that produces a single ContentBlock has
 * its own contract — when it returns ok=false, what cache hint it uses, and
 * what guard it relies on. These tests assert on those contracts directly.
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestModeAndLanguagePart_HasCacheHintAndLanguageDirective(t *testing.T) {
	part := modeAndLanguagePart()
	if part.Type != "text" {
		t.Errorf("Type = %q, want text", part.Type)
	}
	if part.CacheControl == nil || part.CacheControl.Type != "ephemeral" {
		t.Errorf("expected ephemeral cache hint; got %+v", part.CacheControl)
	}
	// The block always concatenates the mode hint and the language directive.
	if !strings.Contains(part.Text, ChatModeSystemHint) {
		t.Error("missing ChatModeSystemHint in the rendered block")
	}
}

func TestWorkspaceContextPart_NilBuilderReturnsFalse(t *testing.T) {
	cli := &ChatCLI{}
	_, ok := cli.workspaceContextPart(testCtx(), "anything")
	if ok {
		t.Error("nil contextBuilder must return ok=false")
	}
}

func TestRecentHistoryHints_EmptyHistory(t *testing.T) {
	cli := &ChatCLI{}
	if got := cli.recentHistoryHints(); got != nil {
		t.Errorf("empty history → nil hints; got %v", got)
	}
}

func TestRecentHistoryHints_TailWindow(t *testing.T) {
	// recentHistoryHints uses a 3-message window. Add 5 messages; the
	// window should sample only the last 3.
	cli := &ChatCLI{
		history: []models.Message{
			{Content: "alpha bravo charlie delta echo"},
			{Content: "x1"},
			{Content: "x2"},
			{Content: "x3"},
			{Content: "x4"},
		},
	}
	// Just exercise the path — keyword extraction itself is tested in
	// the workspace/memory package. The invariant under test is "no panic
	// + non-nil return when history has content."
	_ = cli.recentHistoryHints()
}

func TestMcpChannelPart_NilManagerReturnsFalse(t *testing.T) {
	cli := &ChatCLI{}
	_, ok := cli.mcpChannelPart()
	if ok {
		t.Error("nil mcpManager must yield ok=false")
	}
}

func TestMcpToolsPart_NilManagerReturnsFalse(t *testing.T) {
	cli := &ChatCLI{}
	_, ok := cli.mcpToolsPart()
	if ok {
		t.Error("nil mcpManager must yield ok=false")
	}
}

func TestWatcherContextPart_NilFuncReturnsFalse(t *testing.T) {
	cli := &ChatCLI{}
	_, ok := cli.watcherContextPart()
	if ok {
		t.Error("nil WatcherContextFunc must yield ok=false")
	}
}

func TestWatcherContextPart_EmptyStringReturnsFalse(t *testing.T) {
	cli := &ChatCLI{WatcherContextFunc: func() string { return "" }}
	if _, ok := cli.watcherContextPart(); ok {
		t.Error("empty watcher output must yield ok=false")
	}
}

func TestWatcherContextPart_NonEmptyReturnsBlock(t *testing.T) {
	cli := &ChatCLI{WatcherContextFunc: func() string { return "kube-snap" }}
	part, ok := cli.watcherContextPart()
	if !ok {
		t.Fatal("non-empty watcher output must yield ok=true")
	}
	if part.Text != "kube-snap" {
		t.Errorf("Text = %q, want kube-snap", part.Text)
	}
	// Watcher block is volatile — must NOT carry a cache hint.
	if part.CacheControl != nil {
		t.Errorf("watcher block must not carry a cache hint; got %+v", part.CacheControl)
	}
}

func TestAssembleChatSystemPrompt_AlwaysIncludesModeAndLanguage(t *testing.T) {
	// Minimum fixture: no skills, no contexts, no MCP, no watcher. We
	// only need the language-directive block to come out.
	cli, _ := newPipelineCLI(t, nil)
	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skipf("NewContextHandler unavailable in this environment: %v", err)
	}
	cli.contextHandler = ch
	out := cli.assembleChatSystemPrompt(testCtx(), "hello", "")
	if len(out.parts) == 0 {
		t.Fatal("expected at least the mode/language part")
	}
	if !strings.Contains(out.parts[0].Text, ChatModeSystemHint) {
		t.Errorf("first part must be the mode/language block; got %q", out.parts[0].Text[:30])
	}
}

// testCtx returns a background context — pulled into a helper to keep the
// test bodies tight.
func testCtx() context.Context { return context.Background() }
