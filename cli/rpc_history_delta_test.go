/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * rpc_history_delta_test.go
 *
 * historyTailDelta feeds session-swapped MCP/ACP coder/agent runs into the
 * memory worker (nudgeSegment): the delta must capture the run's new
 * messages, skip system scaffolding, and stay safe under mid-run compaction
 * that rewrites part of the prefix.
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestHistoryTailDelta_AppendedMessages(t *testing.T) {
	before := []models.Message{
		{Role: "user", Content: "old question"},
		{Role: "assistant", Content: "old answer"},
	}
	after := append(append([]models.Message(nil), before...),
		models.Message{Role: "system", Content: "scaffold"},
		models.Message{Role: "user", Content: "new task"},
		models.Message{Role: "assistant", Content: "done"},
	)
	delta := historyTailDelta(before, after)
	if len(delta) != 2 {
		t.Fatalf("expected 2 messages (system skipped), got %d: %+v", len(delta), delta)
	}
	if delta[0].Content != "new task" || delta[1].Content != "done" {
		t.Fatalf("unexpected delta: %+v", delta)
	}
}

func TestHistoryTailDelta_NoNewMessages(t *testing.T) {
	h := []models.Message{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}
	if delta := historyTailDelta(h, h); len(delta) != 0 {
		t.Fatalf("identical histories must yield empty delta, got %+v", delta)
	}
}

func TestHistoryTailDelta_CompactedPrefixOverIncludes(t *testing.T) {
	before := []models.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
	}
	// Compaction rewrote everything past the first message into a summary,
	// then the run appended a new exchange.
	after := []models.Message{
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "[summary of a1/q2]"},
		{Role: "user", Content: "new task"},
		{Role: "assistant", Content: "done"},
	}
	delta := historyTailDelta(before, after)
	if len(delta) != 3 {
		t.Fatalf("rewritten tail must count as new (over-include, never drop), got %+v", delta)
	}
	if delta[len(delta)-1].Content != "done" {
		t.Fatalf("run messages missing from delta: %+v", delta)
	}
}
