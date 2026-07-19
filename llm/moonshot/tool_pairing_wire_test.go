/*
 * ChatCLI - Moonshot wire-shape regression test
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Reproduces the live-monitoring session that Moonshot rejected with
 * "an assistant message with 'tool_calls' must be followed by tool messages
 * responding to each 'tool_call_id'... web_fetch:0, web_fetch:1": Kimi K3
 * reuses deterministic per-turn IDs, an @park suspension left a batch
 * unanswered, and the repaired history must serialize into a message array
 * that satisfies Moonshot's adjacency validation.
 */
package moonshot

import (
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validateMoonshotAdjacency mimics the server-side check: every assistant
// message with tool_calls must be immediately followed by tool messages
// answering each of its tool_call_ids, before any other role appears.
func validateMoonshotAdjacency(t *testing.T, messages []interface{}) {
	t.Helper()
	for i := 0; i < len(messages); i++ {
		m, ok := messages[i].(map[string]interface{})
		require.True(t, ok)
		calls, has := m["tool_calls"].([]map[string]interface{})
		if !has || len(calls) == 0 {
			continue
		}
		pending := make(map[string]bool, len(calls))
		for _, c := range calls {
			pending[c["id"].(string)] = true
		}
		j := i + 1
		for ; j < len(messages); j++ {
			next := messages[j].(map[string]interface{})
			if next["role"] != "tool" {
				break
			}
			delete(pending, next["tool_call_id"].(string))
		}
		assert.Empty(t, pending,
			"assistant message %d has tool_call_ids without adjacent tool responses", i)
	}
}

func TestBuildToolMessages_RepairedParkHistorySatisfiesAdjacency(t *testing.T) {
	kimiCalls := func(ids ...string) []models.ToolCall {
		out := make([]models.ToolCall, 0, len(ids))
		for _, id := range ids {
			out = append(out, models.ToolCall{ID: id, Name: "web_fetch", Type: "function"})
		}
		return out
	}

	history := []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "monitore o jogo"},
		// Cycle 1: fetches answered normally (Kimi per-turn IDs).
		{Role: "assistant", ToolCalls: kimiCalls("web_fetch:0", "web_fetch:1")},
		models.NewToolResultMessage("web_fetch:0", "placar A", false, ""),
		models.NewToolResultMessage("web_fetch:1", "placar B", false, ""),
		// Cycle 2: SAME IDs, batch suspended by @park before emission — the
		// dangling shape the park snapshot used to inherit.
		{Role: "assistant", Content: "report", ToolCalls: kimiCalls("web_fetch:0", "web_fetch:1")},
		// Park resume feedback arrives as a user message.
		{Role: "user", Content: "[@park completed] outcome=elapsed"},
	}

	repaired, report := agent.EnsureToolResultPairing(history, nil)
	require.True(t, report.HasRepairs())

	messages := buildToolMessages("", repaired)
	validateMoonshotAdjacency(t, messages)
}

func TestBuildToolMessages_UnrepairedParkHistoryFailsAdjacency(t *testing.T) {
	// Sanity check on the validator itself: the pre-repair history is the
	// exact shape Moonshot 400s on.
	history := []models.Message{
		{Role: "assistant", ToolCalls: []models.ToolCall{
			{ID: "web_fetch:0", Name: "web_fetch", Type: "function"},
			{ID: "web_fetch:1", Name: "web_fetch", Type: "function"},
		}},
		{Role: "user", Content: "[@park completed] outcome=elapsed"},
	}

	messages := buildToolMessages("", history)

	m := messages[0].(map[string]interface{})
	calls := m["tool_calls"].([]map[string]interface{})
	require.Len(t, calls, 2)
	next := messages[1].(map[string]interface{})
	assert.NotEqual(t, "tool", next["role"],
		"pre-repair history must exhibit the dangling shape the API rejects")
}
