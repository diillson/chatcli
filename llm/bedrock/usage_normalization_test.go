/*
 * ChatCLI - Bedrock usage schema contract
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * All three Bedrock request families must agree on what "the input" is.
 * Converse and the Anthropic InvokeModel/Mantle envelope count cache tokens
 * BESIDE inputTokens; the OpenAI-compatible family (gpt-oss) counts them
 * inside prompt_tokens.
 */
package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockruntimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestConverseUsageIsAdditive(t *testing.T) {
	c := newUsageTestClient(t)
	c.captureConverseUsage(&bedrockruntime.ConverseOutput{
		Usage: &bedrockruntimetypes.TokenUsage{
			InputTokens:           aws.Int32(3),
			OutputTokens:          aws.Int32(53),
			TotalTokens:           aws.Int32(56),
			CacheWriteInputTokens: aws.Int32(19130),
			CacheReadInputTokens:  aws.Int32(0),
		},
		StopReason: bedrockruntimetypes.StopReasonEndTurn,
	})

	u := c.LastUsage()
	if u.InputTokensTotal != 19133 {
		t.Fatalf("InputTokensTotal = %d, want 19133 (3 uncached + 19130 written)", u.InputTokensTotal)
	}
	if u.TotalTokens != 19186 {
		t.Fatalf("TotalTokens = %d, want 19186 — the 56 Converse reports excludes the cache", u.TotalTokens)
	}
}

func TestInvokeModelAnthropicUsageIsAdditive(t *testing.T) {
	c := newUsageTestClient(t)
	c.captureAnthropicUsage([]byte(`{"usage":{"input_tokens":120,"output_tokens":30,
		"cache_creation_input_tokens":10,"cache_read_input_tokens":40}}`))

	if got := c.LastUsage().InputTokensTotal; got != 170 {
		t.Fatalf("InputTokensTotal = %d, want 170", got)
	}
}

func TestOpenAIFamilyUsageIsSubset(t *testing.T) {
	c := newUsageTestClient(t)
	c.captureOpenAIUsage([]byte(`{"usage":{"prompt_tokens":900,"completion_tokens":20,
		"prompt_tokens_details":{"cached_tokens":800}}}`))

	if got := c.LastUsage().InputTokensTotal; got != 900 {
		t.Fatalf("InputTokensTotal = %d, want 900 — gpt-oss keeps OpenAI semantics", got)
	}
}
