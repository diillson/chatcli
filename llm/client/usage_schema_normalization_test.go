/*
 * ChatCLI - Shared usage-parser schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Every provider that speaks an OpenAI- or Anthropic-shaped envelope reads
 * its usage through these three parsers (openai, openairesponses,
 * openaiassistant, xai, zai, moonshot, minimax — both of its surfaces —,
 * copilot, bedrock's InvokeModel bodies and claudeai's buffered path). The
 * contract they must all keep: InputTokensTotal is the whole input of the
 * call, cache included, whatever the schema's split looks like.
 */
package client

import "testing"

func TestParseOpenAIUsage_NormalizesSubsetSchema(t *testing.T) {
	// cached_tokens is a SHARE of prompt_tokens, not an extra charge.
	info := ParseOpenAIUsage(map[string]interface{}{
		"usage": map[string]interface{}{
			"prompt_tokens":         float64(22858),
			"completion_tokens":     float64(28),
			"total_tokens":          float64(22886),
			"prompt_tokens_details": map[string]interface{}{"cached_tokens": float64(10515)},
		},
	})
	if info == nil {
		t.Fatal("no usage parsed")
	}
	if info.InputTokensTotal != 22858 {
		t.Fatalf("InputTokensTotal = %d, want 22858 (cached is already inside)", info.InputTokensTotal)
	}
	if info.TotalTokens != 22886 {
		t.Fatalf("TotalTokens = %d, want 22886", info.TotalTokens)
	}
}

func TestParseOpenAIResponsesUsage_NormalizesSubsetSchema(t *testing.T) {
	info, err := ParseOpenAIResponsesUsage([]byte(`{"usage":{
		"input_tokens": 5000, "output_tokens": 100,
		"input_tokens_details": {"cached_tokens": 4000}}}`))
	if err != nil || info == nil {
		t.Fatalf("parse: %v %+v", err, info)
	}
	if info.InputTokensTotal != 5000 {
		t.Fatalf("InputTokensTotal = %d, want 5000", info.InputTokensTotal)
	}
}

func TestParseAnthropicUsage_NormalizesAdditiveSchema(t *testing.T) {
	// The real shape behind "3 tokens in" on a 19K-token turn.
	info := ParseAnthropicUsage(map[string]interface{}{
		"usage": map[string]interface{}{
			"input_tokens":                float64(3),
			"output_tokens":               float64(53),
			"cache_creation_input_tokens": float64(19130),
			"cache_read_input_tokens":     float64(0),
		},
	})
	if info == nil {
		t.Fatal("no usage parsed")
	}
	if info.InputTokensTotal != 19133 {
		t.Fatalf("InputTokensTotal = %d, want 19133", info.InputTokensTotal)
	}
	if info.TotalTokens != 19186 {
		t.Fatalf("TotalTokens = %d, want 19186", info.TotalTokens)
	}
	if info.PromptTokens != 3 {
		t.Fatalf("PromptTokens = %d — the raw split must survive for cost math", info.PromptTokens)
	}
}

// TestBothSchemasAgreeOnTheSamePrefix is the regression this whole change
// exists for: two providers holding the same ~22K prefix must report the same
// input, however each one splits its counters.
func TestBothSchemasAgreeOnTheSamePrefix(t *testing.T) {
	additive := ParseAnthropicUsage(map[string]interface{}{
		"usage": map[string]interface{}{
			"input_tokens": float64(1000), "output_tokens": float64(10),
			"cache_read_input_tokens": float64(21000),
		},
	})
	subset := ParseOpenAIUsage(map[string]interface{}{
		"usage": map[string]interface{}{
			"prompt_tokens": float64(22000), "completion_tokens": float64(10),
			"prompt_tokens_details": map[string]interface{}{"cached_tokens": float64(21000)},
		},
	})
	if additive.InputTokensTotal != subset.InputTokensTotal {
		t.Fatalf("same prefix reported differently: additive=%d subset=%d",
			additive.InputTokensTotal, subset.InputTokensTotal)
	}
}
