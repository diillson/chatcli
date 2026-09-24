/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmptyResponseErrorNamesTheCause(t *testing.T) {
	refused := &EmptyResponseError{Provider: "ClaudeAI", Model: "m", StopReason: StopReasonRefusal}
	assert.True(t, refused.Refused())
	assert.NotEmpty(t, refused.Error())

	filtered := &EmptyResponseError{Provider: "OpenAI", StopReason: StopReasonContentFilter}
	assert.True(t, filtered.Refused())

	maxed := &EmptyResponseError{Provider: "ClaudeAI", StopReason: StopReasonMaxTokens}
	assert.False(t, maxed.Refused())
	assert.NotEmpty(t, maxed.Error())

	thinking := &EmptyResponseError{Provider: "ClaudeAI", StopReason: "end_turn", Blocks: map[string]int{"thinking": 2}}
	assert.NotEmpty(t, thinking.Error())
	assert.Equal(t, "thinking×2", thinking.blockSummary())

	other := &EmptyResponseError{Provider: "ClaudeAI", StopReason: "tool_use", Blocks: map[string]int{"tool_use": 1, "thinking": 1}}
	assert.Equal(t, "thinking×1, tool_use×1", other.blockSummary(), "sorted, so the message is stable")
	assert.NotEmpty(t, other.Error())

	bare := &EmptyResponseError{}
	assert.Equal(t, "none", bare.blockSummary())
	assert.NotEmpty(t, bare.Error(), "no provider, no stop reason: still a message")

	// With stop_details the message names the category (and the
	// explanation when there is one); without them it stays as before.
	categorised := &EmptyResponseError{Provider: "ClaudeAI", StopReason: StopReasonRefusal,
		Details: &StopDetails{Category: "reasoning_extraction", Explanation: "asked for the chain of thought", RecommendedModel: "claude-opus-4-8"}}
	assert.Equal(t, "reasoning_extraction", categorised.Category())
	assert.Equal(t, "claude-opus-4-8", categorised.RecommendedModel())
	assert.Contains(t, categorised.Error(), "reasoning_extraction")
	assert.Contains(t, categorised.Error(), "asked for the chain of thought")
	terse := &EmptyResponseError{Provider: "ClaudeAI", StopReason: StopReasonRefusal, Details: &StopDetails{Category: "cyber"}}
	assert.Contains(t, terse.Error(), "cyber")
	assert.NotContains(t, terse.Error(), ": )", "no explanation, no dangling separator")
	assert.Empty(t, refused.Category())
	assert.Empty(t, refused.RecommendedModel())
	var none *EmptyResponseError
	assert.Empty(t, none.Category(), "nil-safe")
	assert.Empty(t, none.RecommendedModel(), "nil-safe")
}

func TestAsEmptyResponseUnwraps(t *testing.T) {
	inner := &EmptyResponseError{Provider: "ClaudeAI", StopReason: StopReasonRefusal}
	wrapped := fmt.Errorf("turn 2: %w", inner)
	assert.Same(t, inner, AsEmptyResponse(wrapped))
	assert.True(t, IsRefusal(wrapped))

	assert.Nil(t, AsEmptyResponse(errors.New("plain")))
	assert.False(t, IsRefusal(errors.New("plain")))
	assert.False(t, IsRefusal(nil))
	var none *EmptyResponseError
	assert.False(t, none.Refused(), "nil-safe")
}
