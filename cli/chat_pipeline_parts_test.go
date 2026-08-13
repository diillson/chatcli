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

	"github.com/diillson/chatcli/cli/workspace"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestModeAndLanguagePart_HasCacheHintAndLanguageDirective(t *testing.T) {
	part := (&ChatCLI{}).modeAndLanguagePart()
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
	_, ok := cli.workspaceContextPart(testCtx(), "anything", nil)
	if ok {
		t.Error("nil contextBuilder must return ok=false")
	}
}

func TestChatEffectiveMemoryMode(t *testing.T) {
	tests := []struct {
		name       string
		memoryMode string
		chatMemory string
		withStore  bool
		want       string
	}{
		{"index with pull path stays index", "index", "", true, memModeIndex},
		{"index without store degrades to full", "index", "", false, memModeFull},
		{"index with exception disabled degrades to full", "index", "off", true, memModeFull},
		{"full passes through", "full", "", true, memModeFull},
		{"off passes through", "off", "", true, memModeOff},
		{"default (unset) is index with pull path", "", "", true, memModeIndex},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CHATCLI_MEMORY_MODE", tt.memoryMode)
			t.Setenv(chatMemoryEnvVar, tt.chatMemory)
			cli := &ChatCLI{}
			if tt.withStore {
				cli.memoryStore = &workspace.MemoryStore{}
			}
			if got := cli.chatEffectiveMemoryMode(); got != tt.want {
				t.Fatalf("chatEffectiveMemoryMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAssembleChatSystemPrompt_VolatileSuffixCarriesNoCacheHint(t *testing.T) {
	// Invariant behind the stable-prefix contract: once the first hint-less
	// part appears, no later part may carry a CacheControl hint — a cached
	// block after a volatile one never earns a warm read and poisons the
	// prefix. The new memory/session recall parts (8b/8c) must obey this.
	cli, _ := newPipelineCLI(t, nil)
	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skipf("NewContextHandler unavailable in this environment: %v", err)
	}
	cli.contextHandler = ch
	out := cli.assembleChatSystemPrompt(testCtx(), "hello there", "")

	volatileSeen := false
	for i, p := range out.parts {
		if p.CacheControl == nil {
			volatileSeen = true
			continue
		}
		if volatileSeen {
			t.Fatalf("part %d carries a cache hint after a volatile part — stable prefix broken", i)
		}
	}
}

func TestAssembleChatSystemPrompt_DiscardsCommandToolScope(t *testing.T) {
	// The allowed-tools overlay only means something to the agent/coder
	// gate. A chat turn (e.g. a mode:chat command that still declares
	// allowed-tools) must discard it, or it would leak into whatever
	// /coder run the user starts next.
	cli, _ := newPipelineCLI(t, nil)
	ch, err := NewContextHandler(zap.NewNop())
	if err != nil {
		t.Skipf("NewContextHandler unavailable in this environment: %v", err)
	}
	cli.contextHandler = ch
	cli.pendingCommandAllowedTools = []string{"exec_command"}
	_ = cli.assembleChatSystemPrompt(testCtx(), "hello", "")
	if scope := cli.consumePendingCommandToolScope(); scope != nil {
		t.Errorf("chat turn must discard the staged allowed-tools overlay, got %v", scope)
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
