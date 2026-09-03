/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Guards for the prompt-cache prefix contract and for the reversibility of
 * the guided /compact path (P0 items of the context audit).
 */
package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// The scratch dir is random per process. If its literal path lands in the
// cacheable tools block, every CLI start is a guaranteed cold prompt cache
// for that block and everything after it. The path must ride in the dynamic
// (uncached) line instead.
func TestSessionWorkspaceHint_KeepsRandomPathOutOfCachedBlock(t *testing.T) {
	ws, err := agent.InitSessionWorkspace(zap.NewNop())
	if err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	if ws == nil || ws.ScratchDir == "" {
		t.Fatal("workspace not initialized")
	}

	hint := buildSessionWorkspaceHint()
	if hint == "" {
		t.Fatal("hint must be emitted when a workspace exists")
	}
	if strings.Contains(hint, ws.ScratchDir) {
		t.Fatalf("cached tools block embeds the per-process scratch path:\n%s", hint)
	}
	if !strings.Contains(hint, "CHATCLI_AGENT_TMPDIR") {
		t.Fatal("hint must still teach the env var name")
	}

	line := sessionWorkspaceDynamicLine()
	if !strings.Contains(line, ws.ScratchDir) {
		t.Fatalf("dynamic line must carry the concrete path, got %q", line)
	}
	if !strings.Contains(line, "CHATCLI_AGENT_TMPDIR") {
		t.Fatalf("dynamic line must name the env var, got %q", line)
	}
}

// guidedCompactClient returns a fixed summary and records the prompt it saw.
type guidedCompactClient struct {
	summary string
	prompts []string
}

func (c *guidedCompactClient) GetModelName() string { return "test/model" }
func (c *guidedCompactClient) SendPrompt(_ context.Context, prompt string, _ []models.Message, _ int) (string, error) {
	c.prompts = append(c.prompts, prompt)
	return c.summary, nil
}

// Guided /compact was the only irreversible cut left: it must archive the
// full middle segment in the CCR store (so @recall can restore it) and must
// never split an assistant tool_calls message from its tool results.
func TestGuidedCompact_ArchivesSegmentAndSnapsToolBoundary(t *testing.T) {
	layer := compress.NewLayer(compress.Config{
		Mode:      compress.ModeLossyWithCCR,
		Store:     compress.NewMemoryStore(),
		Threshold: 100,
	})
	c := &guidedCompactClient{summary: "## Decisions\n- keep the auth refactor"}
	cli := &ChatCLI{
		Client:           c,
		logger:           zap.NewNop(),
		compressionLayer: layer,
		history: []models.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "u1 " + strings.Repeat("alpha ", 40)},
			{Role: "assistant", Content: "a1"},
			{Role: "user", Content: "u2"},
			{Role: "assistant", Content: "a2"},
			{Role: "assistant", Content: "calling", ToolCalls: []models.ToolCall{{ID: "tc1", Name: "@read"}}},
			{Role: "tool", Content: "file bytes", ToolCallID: "tc1"},
			{Role: "tool", Content: "more bytes", ToolCallID: "tc2"},
			{Role: "user", Content: "u4"},
			{Role: "assistant", Content: "a4"},
		},
	}

	cli.guidedCompact(context.Background(), "keep the auth decisions")

	// len=10, keep 4 → naive cut at index 6 (a tool result) → snapped to 5.
	if len(cli.history) != 7 {
		t.Fatalf("expected system + summary + 5 kept messages, got %d", len(cli.history))
	}
	if cli.history[0].Role != "system" {
		t.Fatalf("system prefix lost: %+v", cli.history[0])
	}
	summary := cli.history[1]
	if summary.Meta == nil || !summary.Meta.IsSummary || summary.Meta.SummaryOf != 4 {
		t.Fatalf("summary meta wrong: %+v", summary.Meta)
	}
	if cli.history[2].Role != "assistant" || len(cli.history[2].ToolCalls) != 1 {
		t.Fatalf("tool block head must stay in the verbatim tail, got %+v", cli.history[2])
	}
	if !strings.Contains(summary.Content, c.summary) {
		t.Fatalf("summary content missing model output: %q", summary.Content)
	}

	// The archived original is recoverable through the marker in the note.
	idx := strings.Index(summary.Content, "<<ccr:")
	if idx < 0 {
		t.Fatalf("summary carries no @recall marker:\n%s", summary.Content)
	}
	end := strings.Index(summary.Content[idx:], ">>")
	if end < 0 {
		t.Fatal("malformed ccr marker")
	}
	key := strings.TrimPrefix(summary.Content[idx:idx+end], "<<ccr:")
	original, ok := layer.Recall(key)
	if !ok {
		t.Fatalf("archived segment not recallable for key %q", key)
	}
	for _, want := range []string{"u1 alpha", "[assistant]: a2"} {
		if !strings.Contains(original, want) {
			t.Fatalf("archive missing %q:\n%s", want, original)
		}
	}
	// The tool block (assistant calling + its results) was snapped into the
	// verbatim tail, so it must NOT be in the archived middle segment.
	for _, tail := range []string{"[assistant]: calling", "file bytes"} {
		if strings.Contains(original, tail) {
			t.Fatalf("archive must not include the kept tail %q", tail)
		}
	}
}

// Without a CCR layer the legacy behavior is kept verbatim: no marker, no
// panic, same reconstruction.
func TestGuidedCompact_NoLayerKeepsLegacyShape(t *testing.T) {
	cli := &ChatCLI{
		Client: &guidedCompactClient{summary: "note"},
		logger: zap.NewNop(),
		history: []models.Message{
			{Role: "user", Content: "u1"},
			{Role: "assistant", Content: "a1"},
			{Role: "user", Content: "u2"},
			{Role: "assistant", Content: "a2"},
			{Role: "user", Content: "u3"},
			{Role: "assistant", Content: "a3"},
			{Role: "user", Content: "u4"},
			{Role: "assistant", Content: "a4"},
		},
	}
	cli.guidedCompact(context.Background(), "keep everything")
	if len(cli.history) != 5 {
		t.Fatalf("expected summary + 4 kept, got %d", len(cli.history))
	}
	if strings.Contains(cli.history[0].Content, "<<ccr:") {
		t.Fatal("no layer configured, yet a marker was emitted")
	}
}
