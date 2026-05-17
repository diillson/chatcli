/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package claudeai

import (
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildClaudeToolMessages_EmitsNativeIsError verifies that when
// the agent layer marked a tool result as an error, the Anthropic
// adapter forwards is_error=true on the tool_result content block.
// This is the wire-level contract Anthropic uses to flag failure on
// the LLM side so the model can reason about retryability without
// re-parsing the body.
func TestBuildClaudeToolMessages_EmitsNativeIsError(t *testing.T) {
	history := []models.Message{
		{
			Role: "assistant",
			ToolCalls: []models.ToolCall{
				{ID: "toolu_1", Name: "read_file"},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "toolu_1",
			Content:    "could not open file",
			IsError:    true,
			ErrorCode:  "ENOENT",
		},
	}
	msgs := buildClaudeToolMessages("", history)
	require.NotEmpty(t, msgs)

	// Find the user message containing the tool_result block.
	var toolResultBlock map[string]interface{}
	for _, m := range msgs {
		envelope, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		if envelope["role"] != "user" {
			continue
		}
		blocks, ok := envelope["content"].([]map[string]interface{})
		if !ok {
			continue
		}
		for _, b := range blocks {
			if b["type"] == "tool_result" {
				toolResultBlock = b
				break
			}
		}
	}
	require.NotNil(t, toolResultBlock, "expected a tool_result content block in the user message")

	assert.Equal(t, "toolu_1", toolResultBlock["tool_use_id"])
	assert.Equal(t, true, toolResultBlock["is_error"],
		"Anthropic adapter must set is_error=true natively when the agent flagged the result as an error")
	// Content carries the [ERROR:<code>] marker prefix as a defensive
	// extra signal in case the model doesn't read is_error.
	assert.Equal(t, "[ERROR:ENOENT] could not open file", toolResultBlock["content"])
}

// TestBuildClaudeToolMessages_NoErrorPassesThrough is the inverse:
// non-error tool messages emit a plain tool_result with no is_error
// field and no marker prefix. We don't want successful results to be
// polluted by an [ERROR] tag.
func TestBuildClaudeToolMessages_NoErrorPassesThrough(t *testing.T) {
	history := []models.Message{
		{
			Role: "assistant",
			ToolCalls: []models.ToolCall{
				{ID: "toolu_42", Name: "search"},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "toolu_42",
			Content:    "found 3 matches",
		},
	}
	msgs := buildClaudeToolMessages("", history)
	require.NotEmpty(t, msgs)

	var toolResultBlock map[string]interface{}
	for _, m := range msgs {
		envelope, ok := m.(map[string]interface{})
		if !ok || envelope["role"] != "user" {
			continue
		}
		blocks, ok := envelope["content"].([]map[string]interface{})
		if !ok {
			continue
		}
		for _, b := range blocks {
			if b["type"] == "tool_result" {
				toolResultBlock = b
				break
			}
		}
	}
	require.NotNil(t, toolResultBlock)
	assert.Equal(t, "found 3 matches", toolResultBlock["content"])
	_, hasIsError := toolResultBlock["is_error"]
	assert.False(t, hasIsError, "non-error tool_result must not carry is_error")
}

// TestBuildClaudeToolMessages_ErrorWithoutCodeStillSetsFlag covers the
// case where the agent flagged an error but didn't classify a code.
// Anthropic still receives is_error=true; the content stays uncluttered.
func TestBuildClaudeToolMessages_ErrorWithoutCodeStillSetsFlag(t *testing.T) {
	history := []models.Message{
		{
			Role:       "tool",
			ToolCallID: "toolu_x",
			Content:    "something failed",
			IsError:    true,
		},
	}
	msgs := buildClaudeToolMessages("", history)
	require.NotEmpty(t, msgs)

	envelope := msgs[0].(map[string]interface{})
	blocks := envelope["content"].([]map[string]interface{})
	assert.Equal(t, true, blocks[0]["is_error"])
	assert.Equal(t, "something failed", blocks[0]["content"],
		"no ErrorCode means no marker prefix — content stays clean")
}
