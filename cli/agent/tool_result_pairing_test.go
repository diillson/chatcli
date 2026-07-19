/*
 * ChatCLI - Tool Result Pairing Validator tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The duplicate-ID scenarios mirror Moonshot/Kimi K3, whose tool_call IDs
 * are deterministic per turn ("web_fetch:0", "web_fetch:1") and repeat
 * across turns — the exact shape that made the previous global-map pairing
 * ship dangling tool_calls and got rejected by the API with a 400.
 */
package agent

import (
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assistantWithCalls(ids ...string) models.Message {
	calls := make([]models.ToolCall, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, models.ToolCall{ID: id, Name: "web_fetch", Type: "function"})
	}
	return models.Message{Role: "assistant", ToolCalls: calls}
}

func toolResult(id, content string) models.Message {
	return models.NewToolResultMessage(id, content, false, "")
}

func TestEnsureToolResultPairing_ValidHistoryUnchanged(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "go"},
		assistantWithCalls("web_fetch:0", "web_fetch:1"),
		toolResult("web_fetch:0", "a"),
		toolResult("web_fetch:1", "b"),
		{Role: "assistant", Content: "done"},
	}

	repaired, report := EnsureToolResultPairing(history, nil)

	assert.False(t, report.HasRepairs())
	assert.Equal(t, history, repaired)
}

func TestEnsureToolResultPairing_DuplicateIDsAcrossTurns_SecondBlockDangling(t *testing.T) {
	// Turn A answered; turn B reuses the SAME IDs (Kimi K3) but its results
	// were lost. The old global-map pairing saw turn A's results and injected
	// nothing → API 400. Block-scoped pairing must fix turn B.
	history := []models.Message{
		{Role: "user", Content: "go"},
		assistantWithCalls("web_fetch:0", "web_fetch:1"),
		toolResult("web_fetch:0", "a"),
		toolResult("web_fetch:1", "b"),
		assistantWithCalls("web_fetch:0", "web_fetch:1"),
		{Role: "user", Content: "[batch feedback]"},
	}

	repaired, report := EnsureToolResultPairing(history, nil)

	assert.Equal(t, 2, report.SyntheticResultsInjected)
	assert.Zero(t, report.OrphanResultsRemoved)
	require.Len(t, repaired, 8)
	// Turn B must be immediately followed by its two synthetic results.
	assert.Equal(t, "tool", repaired[5].Role)
	assert.Equal(t, "web_fetch:0", repaired[5].ToolCallID)
	assert.Equal(t, SyntheticToolResultContent, repaired[5].Content)
	assert.Equal(t, "tool", repaired[6].Role)
	assert.Equal(t, "web_fetch:1", repaired[6].ToolCallID)
	assert.Equal(t, "user", repaired[7].Role)
	// Turn A keeps its real results.
	assert.Equal(t, "a", repaired[2].Content)
	assert.Equal(t, "b", repaired[3].Content)
}

func TestEnsureToolResultPairing_DanglingFirstBlock_LaterBlockAnswered(t *testing.T) {
	// The park-session shape: an OLD dangling block, then a later turn with
	// the same IDs properly answered. The old code paired the old block with
	// the new results (global map) and repaired nothing.
	history := []models.Message{
		assistantWithCalls("web_fetch:0", "web_fetch:1"),
		{Role: "user", Content: "[legacy batch feedback]"},
		assistantWithCalls("web_fetch:0", "web_fetch:1"),
		toolResult("web_fetch:0", "fresh-a"),
		toolResult("web_fetch:1", "fresh-b"),
	}

	repaired, report := EnsureToolResultPairing(history, nil)

	assert.Equal(t, 2, report.SyntheticResultsInjected)
	require.Len(t, repaired, 7)
	// Old block gets synthetics, adjacent.
	assert.Equal(t, SyntheticToolResultContent, repaired[1].Content)
	assert.Equal(t, SyntheticToolResultContent, repaired[2].Content)
	assert.Equal(t, "user", repaired[3].Role)
	// New block keeps its own results.
	assert.Equal(t, "fresh-a", repaired[5].Content)
	assert.Equal(t, "fresh-b", repaired[6].Content)
}

func TestEnsureToolResultPairing_InterposedMessageRelocatesResults(t *testing.T) {
	// Mid-batch feedback appended between the assistant tool_calls and the
	// results breaks provider adjacency; the results must be pulled back
	// next to their calls and the feedback pushed after them.
	history := []models.Message{
		assistantWithCalls("web_fetch:0", "web_fetch:1"),
		{Role: "user", Content: "SECURITY BLOCK: something"},
		toolResult("web_fetch:0", "a"),
		toolResult("web_fetch:1", "b"),
	}

	repaired, report := EnsureToolResultPairing(history, nil)

	assert.Equal(t, 2, report.ResultsRelocated)
	assert.Zero(t, report.SyntheticResultsInjected)
	require.Len(t, repaired, 4)
	assert.Equal(t, "tool", repaired[1].Role)
	assert.Equal(t, "a", repaired[1].Content)
	assert.Equal(t, "tool", repaired[2].Role)
	assert.Equal(t, "b", repaired[2].Content)
	assert.Equal(t, "user", repaired[3].Role)
}

func TestEnsureToolResultPairing_OrphanResultRemoved(t *testing.T) {
	history := []models.Message{
		{Role: "user", Content: "go"},
		toolResult("ghost:0", "nobody asked"),
		{Role: "assistant", Content: "done"},
	}

	repaired, report := EnsureToolResultPairing(history, nil)

	assert.Equal(t, 1, report.OrphanResultsRemoved)
	assert.Equal(t, []string{"ghost:0"}, report.OrphanToolResultIDs)
	require.Len(t, repaired, 2)
	assert.Equal(t, "user", repaired[0].Role)
	assert.Equal(t, "assistant", repaired[1].Role)
}

func TestEnsureToolResultPairing_PartialResultsGetSynthetics(t *testing.T) {
	history := []models.Message{
		assistantWithCalls("web_fetch:0", "web_fetch:1", "park:2"),
		toolResult("web_fetch:0", "only this ran"),
	}

	repaired, report := EnsureToolResultPairing(history, nil)

	assert.Equal(t, 2, report.SyntheticResultsInjected)
	assert.ElementsMatch(t, []string{"web_fetch:1", "park:2"}, report.MissingToolUseIDs)
	require.Len(t, repaired, 4)
	// Emitted in call order: real, synthetic, synthetic.
	assert.Equal(t, "web_fetch:0", repaired[1].ToolCallID)
	assert.Equal(t, "only this ran", repaired[1].Content)
	assert.Equal(t, "web_fetch:1", repaired[2].ToolCallID)
	assert.Equal(t, "park:2", repaired[3].ToolCallID)
}

func TestEnsureToolResultPairing_IntraMessageDuplicatePruned(t *testing.T) {
	history := []models.Message{
		assistantWithCalls("dup:0", "dup:0"),
		toolResult("dup:0", "once"),
	}

	repaired, report := EnsureToolResultPairing(history, nil)

	assert.Equal(t, 1, report.DuplicateToolUsePruned)
	require.Len(t, repaired, 2)
	require.Len(t, repaired[0].ToolCalls, 1)
	assert.Equal(t, "once", repaired[1].Content)
}

func TestEnsureToolResultPairing_ResultBeyondNextAssistantNotClaimed(t *testing.T) {
	// A result sitting past the NEXT assistant message belongs to that later
	// block's stretch, never to the earlier one.
	history := []models.Message{
		assistantWithCalls("web_fetch:0"),
		{Role: "assistant", Content: "interleaved"},
		toolResult("web_fetch:0", "late"),
	}

	repaired, report := EnsureToolResultPairing(history, nil)

	assert.Equal(t, 1, report.SyntheticResultsInjected)
	assert.Equal(t, 1, report.OrphanResultsRemoved)
	require.Len(t, repaired, 3)
	assert.Equal(t, SyntheticToolResultContent, repaired[1].Content)
	assert.Equal(t, "interleaved", repaired[2].Content)
}

func TestCountPendingToolCalls_LastBlockScoped(t *testing.T) {
	history := []models.Message{
		assistantWithCalls("web_fetch:0", "web_fetch:1"),
		toolResult("web_fetch:0", "a"),
	}
	assert.Equal(t, 1, CountPendingToolCalls(history))

	history = append(history, toolResult("web_fetch:1", "b"))
	assert.Equal(t, 0, CountPendingToolCalls(history))
}
