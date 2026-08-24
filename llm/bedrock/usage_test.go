/*
 * ChatCLI - Bedrock usage capture tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockruntimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"go.uber.org/zap"
)

func newUsageTestClient(t *testing.T) *BedrockClient {
	t.Helper()
	return NewBedrockClient("anthropic.claude-sonnet-5", "us-east-1", "", zap.NewNop(), 1, 0)
}

// TestCaptureAnthropicUsage covers InvokeModel and Mantle (same envelope).
func TestCaptureAnthropicUsage(t *testing.T) {
	c := newUsageTestClient(t)
	body := []byte(`{
		"content": [{"type":"text","text":"hi"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 120, "output_tokens": 30,
		          "cache_creation_input_tokens": 10, "cache_read_input_tokens": 40}
	}`)
	c.captureAnthropicUsage(body)

	u := c.LastUsage()
	if u == nil || !u.IsReal {
		t.Fatalf("usage not captured as real: %+v", u)
	}
	if u.PromptTokens != 120 || u.CompletionTokens != 30 ||
		u.CacheCreationInputTokens != 10 || u.CacheReadInputTokens != 40 {
		t.Fatalf("wrong counts: %+v", u)
	}
	if c.LastStopReason() != "end_turn" {
		t.Fatalf("stop reason = %q", c.LastStopReason())
	}
}

// TestCaptureConverseUsage covers the typed Converse TokenUsage.
func TestCaptureConverseUsage(t *testing.T) {
	c := newUsageTestClient(t)
	out := &bedrockruntime.ConverseOutput{
		Usage: &bedrockruntimetypes.TokenUsage{
			InputTokens:          aws.Int32(200),
			OutputTokens:         aws.Int32(50),
			TotalTokens:          aws.Int32(250),
			CacheReadInputTokens: aws.Int32(80),
		},
		StopReason: bedrockruntimetypes.StopReasonEndTurn,
	}
	c.captureConverseUsage(out)

	u := c.LastUsage()
	if u == nil || u.PromptTokens != 200 || u.CompletionTokens != 50 || u.TotalTokens != 250 ||
		u.CacheReadInputTokens != 80 || !u.IsReal {
		t.Fatalf("wrong converse usage: %+v", u)
	}
}

// TestCaptureOpenAIUsage covers the gpt-oss family body.
func TestCaptureOpenAIUsage(t *testing.T) {
	c := newUsageTestClient(t)
	body := []byte(`{
		"choices": [{"message": {"content": "hi"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 90, "completion_tokens": 12, "total_tokens": 102}
	}`)
	c.captureOpenAIUsage(body)

	u := c.LastUsage()
	if u == nil || u.PromptTokens != 90 || u.CompletionTokens != 12 || !u.IsReal {
		t.Fatalf("wrong openai-family usage: %+v", u)
	}
	if c.LastStopReason() != "stop" {
		t.Fatalf("stop reason = %q", c.LastStopReason())
	}
}

// TestResetUsageClearsStaleState: an errored call must not re-report the
// previous call's tokens.
func TestResetUsageClearsStaleState(t *testing.T) {
	c := newUsageTestClient(t)
	c.captureAnthropicUsage([]byte(`{"usage": {"input_tokens": 5, "output_tokens": 5}}`))
	if c.LastUsage() == nil {
		t.Fatal("precondition: usage set")
	}
	c.resetUsage()
	if c.LastUsage() != nil {
		t.Fatal("resetUsage left stale usage behind")
	}
}
