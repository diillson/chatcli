/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestMirrorContextEdits_StubsOldestKeepsRecentBooksAndNotesRebuild(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker()}
	for i := 0; i < 8; i++ {
		c.history = append(c.history,
			models.Message{Role: "assistant", Content: "run", ToolCalls: []models.ToolCall{{ID: "c", Name: "read"}}},
			models.Message{Role: "tool", ToolCallID: "c", Content: strings.Repeat("output ", 200)})
	}
	n := c.mirrorContextEdits(&models.ContextEdits{ClearedToolUses: 2, ClearedInputTokens: 9000})
	if n != 2 {
		t.Fatalf("stubbed = %d, want the two oldest results", n)
	}
	if !strings.HasPrefix(c.history[1].Content, clearedToolResultStub) || !strings.HasPrefix(c.history[3].Content, clearedToolResultStub) {
		t.Fatal("the oldest tool results must be stubbed")
	}
	if strings.HasPrefix(c.history[5].Content, clearedToolResultStub) {
		t.Fatal("a result the server kept must stay")
	}
	edits, uses, tokens := c.costTracker.ContextEditStats()
	if edits != 1 || uses != 2 || tokens != 9000 {
		t.Fatalf("stats = %d %d %d", edits, uses, tokens)
	}
	if !c.costTracker.cache.rebuildPending {
		t.Fatal("a local stub changes the prefix: the next write is an expected rebuild")
	}
	// Server keeps the five most recent: with six groups and a claim of ten
	// clears, only one is clearable; idempotent on a second pass.
	c2 := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker()}
	for i := 0; i < 6; i++ {
		c2.history = append(c2.history,
			models.Message{Role: "assistant", Content: "run", ToolCalls: []models.ToolCall{{ID: "c", Name: "read"}}},
			models.Message{Role: "tool", ToolCallID: "c", Content: "out"})
	}
	if n := c2.mirrorContextEdits(&models.ContextEdits{ClearedToolUses: 10}); n != 1 {
		t.Fatalf("keep threshold: stubbed=%d", n)
	}
	if n := c2.mirrorContextEdits(&models.ContextEdits{ClearedToolUses: 10}); n != 0 {
		t.Fatalf("second pass must be idempotent: %d", n)
	}
	if c.mirrorContextEdits(nil) != 0 || (*ChatCLI)(nil).mirrorContextEdits(&models.ContextEdits{ClearedToolUses: 1}) != 0 {
		t.Fatal("nil-safe")
	}
}
