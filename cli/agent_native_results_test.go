/*
 * ChatCLI - Native tool_call result emission helpers tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nativeCalls(ids ...string) []models.ToolCall {
	calls := make([]models.ToolCall, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, models.ToolCall{ID: id, Name: "web_fetch", Type: "function"})
	}
	return calls
}

func TestInsertStructuredToolResults_BeforeMidBatchFeedback(t *testing.T) {
	history := []models.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: nativeCalls("web_fetch:0", "web_fetch:1")},
		{Role: "user", Content: "SECURITY BLOCK: denied"},
	}
	results := []models.Message{
		models.NewToolResultMessage("web_fetch:0", "ok", false, ""),
		models.NewToolResultMessage("web_fetch:1", "[not executed]", true, "not_executed"),
	}

	out := insertStructuredToolResults(history, results)

	require.Len(t, out, 5)
	assert.Equal(t, "assistant", out[1].Role)
	assert.Equal(t, "web_fetch:0", out[2].ToolCallID)
	assert.Equal(t, "web_fetch:1", out[3].ToolCallID)
	assert.Equal(t, "SECURITY BLOCK: denied", out[4].Content)
}

func TestInsertStructuredToolResults_AfterExistingToolRun(t *testing.T) {
	history := []models.Message{
		{Role: "assistant", ToolCalls: nativeCalls("a:0", "a:1")},
		models.NewToolResultMessage("a:0", "already there", false, ""),
	}
	results := []models.Message{
		models.NewToolResultMessage("a:1", "late", false, ""),
	}

	out := insertStructuredToolResults(history, results)

	require.Len(t, out, 3)
	assert.Equal(t, "a:0", out[1].ToolCallID)
	assert.Equal(t, "a:1", out[2].ToolCallID)
}

func TestInsertStructuredToolResults_NoAnchorAppends(t *testing.T) {
	history := []models.Message{{Role: "user", Content: "hi"}}
	results := []models.Message{models.NewToolResultMessage("x:0", "y", false, "")}

	out := insertStructuredToolResults(history, results)

	require.Len(t, out, 2)
	assert.Equal(t, "x:0", out[1].ToolCallID)
}

func TestInsertStructuredToolResults_EmptyResultsNoop(t *testing.T) {
	history := []models.Message{{Role: "user", Content: "hi"}}
	assert.Equal(t, history, insertStructuredToolResults(history, nil))
}

func TestBuildNativeBatchResults_FailFastTail(t *testing.T) {
	calls := nativeCalls("web_fetch:0", "web_fetch:1", "web_fetch:2")
	executed := []agent.ToolResult{
		{Output: "ok"},
		{Output: "boom", IsError: true, ErrorCode: "network"},
	}

	results := buildNativeBatchResults(calls, executed, toolResultNotExecutedBatchError)

	require.Len(t, results, 3)
	assert.Equal(t, "ok", results[0].Content)
	assert.False(t, results[0].IsError)
	assert.Equal(t, "boom", results[1].Content)
	assert.True(t, results[1].IsError)
	assert.Equal(t, "network", results[1].ErrorCode)
	assert.Equal(t, toolResultNotExecutedBatchError, results[2].Content)
	assert.True(t, results[2].IsError)
	assert.Equal(t, toolResultErrorCodeNotExecuted, results[2].ErrorCode)
}

func TestBuildParkBatchClosure_ClosesEverythingExceptPark(t *testing.T) {
	// The park session shape: [web_fetch:0, web_fetch:1, park:2] — the two
	// fetches executed, park suspends the loop. The closure must answer the
	// fetches and leave ONLY the park call pending for resume.
	calls := nativeCalls("web_fetch:0", "web_fetch:1")
	calls = append(calls, models.ToolCall{ID: "park:2", Name: "park", Type: "function"})
	executed := []agent.ToolResult{
		{Output: "placar 1"},
		{Output: "placar 2"},
	}

	results := buildParkBatchClosure(calls, executed, 2)

	require.Len(t, results, 2)
	assert.Equal(t, "web_fetch:0", results[0].ToolCallID)
	assert.Equal(t, "placar 1", results[0].Content)
	assert.Equal(t, "web_fetch:1", results[1].ToolCallID)
	assert.Equal(t, "placar 2", results[1].Content)
}

func TestBuildParkBatchClosure_ParkFirstRestNotExecuted(t *testing.T) {
	calls := nativeCalls("park:0", "web_fetch:1")

	results := buildParkBatchClosure(calls, nil, 0)

	require.Len(t, results, 1)
	assert.Equal(t, "web_fetch:1", results[0].ToolCallID)
	assert.Equal(t, toolResultNotExecutedPark, results[0].Content)
	assert.True(t, results[0].IsError)
}

func TestBuildParkBatchClosure_SingleParkCallEmpty(t *testing.T) {
	calls := []models.ToolCall{{ID: "park:0", Name: "park"}}
	assert.Empty(t, buildParkBatchClosure(calls, nil, 0))
}

// A native @recall result must carry PreserveVerbatim (the XML feedback path
// already does via buildBatchFeedbackMessage): it is an archived original the
// model asked to see in full, and trimming/microcompact would otherwise force
// another recall.
func TestBuildNativeBatchResults_RecallMarkedPreserveVerbatim(t *testing.T) {
	calls := []models.ToolCall{
		{ID: "c1", Name: "@recall"},
		{ID: "c2", Name: "@read"},
	}
	executed := []agent.ToolResult{
		{Output: "original bytes"},
		{Output: "file bytes"},
	}
	results := buildNativeBatchResults(calls, executed, toolResultNotExecutedBatchError)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Meta == nil || !results[0].Meta.PreserveVerbatim {
		t.Fatalf("@recall result must be PreserveVerbatim, got meta=%+v", results[0].Meta)
	}
	if results[1].Meta != nil {
		t.Fatalf("non-recall result must not carry meta, got %+v", results[1].Meta)
	}
}
