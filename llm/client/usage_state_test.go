/*
 * ChatCLI - Tests for usage parsing
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Chat Completions and the Responses API ship different field names for
// the same concept (prompt_tokens vs input_tokens). Both parsers normalize
// to the same UsageInfo, so the chat envelope's formatTokenSummary can
// stay provider-agnostic. These tests pin each schema independently so a
// silent rename upstream surfaces here instead of as a "no tokens"
// placeholder in the UI.

func TestParseOpenAIUsage_ChatCompletions(t *testing.T) {
	const body = `{
		"usage": {
			"prompt_tokens": 1500,
			"completion_tokens": 200,
			"total_tokens": 1700,
			"prompt_tokens_details": {"cached_tokens": 1024},
			"completion_tokens_details": {"reasoning_tokens": 64}
		}
	}`
	var raw map[string]interface{}
	assert.NoError(t, json.Unmarshal([]byte(body), &raw))

	got := ParseOpenAIUsage(raw)
	assert.NotNil(t, got, "Chat Completions usage block must parse")
	assert.True(t, got.IsReal)
	assert.Equal(t, 1500, got.PromptTokens)
	assert.Equal(t, 200, got.CompletionTokens)
	assert.Equal(t, 1700, got.TotalTokens)
	assert.Equal(t, 1024, got.CacheReadInputTokens, "prompt_tokens_details.cached_tokens → CacheReadInputTokens")
	assert.Equal(t, 64, got.ReasoningTokens, "completion_tokens_details.reasoning_tokens → ReasoningTokens")
}

func TestParseOpenAIUsage_MissingDetailsIsFine(t *testing.T) {
	// Older models (gpt-4 / gpt-3.5) omit the *_details sub-objects.
	const body = `{
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
	}`
	var raw map[string]interface{}
	assert.NoError(t, json.Unmarshal([]byte(body), &raw))

	got := ParseOpenAIUsage(raw)
	assert.NotNil(t, got)
	assert.Equal(t, 10, got.PromptTokens)
	assert.Equal(t, 5, got.CompletionTokens)
	assert.Equal(t, 15, got.TotalTokens)
	assert.Zero(t, got.CacheReadInputTokens)
	assert.Zero(t, got.ReasoningTokens)
}

func TestParseOpenAIUsage_ComputesTotalWhenMissing(t *testing.T) {
	const body = `{"usage": {"prompt_tokens": 8, "completion_tokens": 4}}`
	var raw map[string]interface{}
	assert.NoError(t, json.Unmarshal([]byte(body), &raw))

	got := ParseOpenAIUsage(raw)
	assert.NotNil(t, got)
	assert.Equal(t, 12, got.TotalTokens, "total must be computed from prompt+completion when API omits it")
}

func TestParseOpenAIUsage_NoUsageBlockReturnsNil(t *testing.T) {
	got := ParseOpenAIUsage(map[string]interface{}{"choices": []interface{}{}})
	assert.Nil(t, got, "absent usage block must yield nil, not a zeroed UsageInfo")
}

func TestParseOpenAIResponsesUsage(t *testing.T) {
	// Responses API uses input_tokens / output_tokens — NOT prompt_/completion_.
	// Reasoning models populate output_tokens_details.reasoning_tokens (already
	// counted inside output_tokens).
	const body = `{
		"usage": {
			"input_tokens": 75,
			"input_tokens_details": {"cached_tokens": 1024},
			"output_tokens": 1186,
			"output_tokens_details": {"reasoning_tokens": 1024},
			"total_tokens": 1261
		}
	}`

	got, err := ParseOpenAIResponsesUsage([]byte(body))
	assert.NoError(t, err)
	assert.NotNil(t, got, "Responses API usage block must parse")
	assert.True(t, got.IsReal)
	assert.Equal(t, 75, got.PromptTokens, "input_tokens → PromptTokens")
	assert.Equal(t, 1186, got.CompletionTokens, "output_tokens → CompletionTokens")
	assert.Equal(t, 1261, got.TotalTokens)
	assert.Equal(t, 1024, got.CacheReadInputTokens, "input_tokens_details.cached_tokens → CacheReadInputTokens")
	assert.Equal(t, 1024, got.ReasoningTokens, "output_tokens_details.reasoning_tokens → ReasoningTokens")
}

func TestParseOpenAIResponsesUsage_ComputesTotalWhenMissing(t *testing.T) {
	got, err := ParseOpenAIResponsesUsage([]byte(`{"usage":{"input_tokens":8,"output_tokens":4}}`))
	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Equal(t, 12, got.TotalTokens, "total must fall back to input+output when API omits it")
}

func TestParseOpenAIResponsesUsage_DetailsAreOptional(t *testing.T) {
	got, err := ParseOpenAIResponsesUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`))
	assert.NoError(t, err)
	assert.NotNil(t, got)
	assert.Zero(t, got.CacheReadInputTokens, "missing input_tokens_details must not break the parse")
	assert.Zero(t, got.ReasoningTokens, "missing output_tokens_details must not break the parse")
}

// Regression: before the split, openai_responses_client.go called
// ParseOpenAIUsage on a Responses payload — it returned a zeroed
// UsageInfo because prompt_tokens/completion_tokens never appear in that
// schema, and the chat envelope rendered the "no tokens" placeholder
// instead of the input/output arrows.
func TestParseOpenAIUsage_ReturnsZerosOnResponsesPayload(t *testing.T) {
	const body = `{"usage": {"input_tokens": 75, "output_tokens": 1186, "total_tokens": 1261}}`
	var raw map[string]interface{}
	assert.NoError(t, json.Unmarshal([]byte(body), &raw))

	got := ParseOpenAIUsage(raw)
	assert.NotNil(t, got, "the usage block is present so we still return a struct")
	assert.Zero(t, got.PromptTokens, "Chat Completions parser must not pick up input_tokens — that's what ParseOpenAIResponsesUsage is for")
	assert.Zero(t, got.CompletionTokens)
}

func TestParseOpenAIResponsesUsage_NoUsageBlockReturnsNil(t *testing.T) {
	got, err := ParseOpenAIResponsesUsage([]byte(`{"status":"completed"}`))
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestParseOpenAIResponsesUsage_EmptyInputReturnsNil(t *testing.T) {
	got, err := ParseOpenAIResponsesUsage(nil)
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestParseOpenAIResponsesUsage_InvalidJSONReturnsError(t *testing.T) {
	// Invalid JSON must surface as an error rather than a silent nil —
	// the caller (responses client) logs it at debug level so silent
	// schema breakage shows up in support traces.
	got, err := ParseOpenAIResponsesUsage([]byte(`{"usage": not-json`))
	assert.Error(t, err)
	assert.Nil(t, got)
}

// TestParseAnthropicUsage_StaysUntouched is a guard: the Anthropic parser
// must not start picking up OpenAI-shaped fields just because we added the
// Responses parser next door. Different fields, different sources.
func TestParseAnthropicUsage_StaysUntouched(t *testing.T) {
	const body = `{
		"usage": {
			"input_tokens": 100, "output_tokens": 50,
			"cache_creation_input_tokens": 20, "cache_read_input_tokens": 80
		}
	}`
	var raw map[string]interface{}
	assert.NoError(t, json.Unmarshal([]byte(body), &raw))

	got := ParseAnthropicUsage(raw)
	assert.NotNil(t, got)
	assert.Equal(t, 100, got.PromptTokens)
	assert.Equal(t, 50, got.CompletionTokens)
	assert.Equal(t, 20, got.CacheCreationInputTokens)
	assert.Equal(t, 80, got.CacheReadInputTokens)
	assert.Equal(t, 150, got.TotalTokens, "Anthropic parser sums input+output (API doesn't include total)")
}
