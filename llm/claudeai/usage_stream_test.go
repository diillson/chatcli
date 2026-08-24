/*
 * ChatCLI - Claude stream usage accumulator tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"testing"
)

// TestStreamUsageAccumulator drives the OAuth-stream event sequence:
// message_start carries input/cache counts, message_delta the final output
// count and stop reason.
func TestStreamUsageAccumulator(t *testing.T) {
	var acc streamUsageAccumulator

	acc.observe([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":150,"output_tokens":1,"cache_creation_input_tokens":20,"cache_read_input_tokens":90}}}`))
	acc.observe([]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`))
	acc.observe([]byte(`{"type":"message_delta","usage":{"output_tokens":37},"delta":{"stop_reason":"end_turn"}}`))

	c := &ClaudeClient{}
	acc.commit(c)

	u := c.LastUsage()
	if u == nil || !u.IsReal {
		t.Fatalf("no real usage committed: %+v", u)
	}
	if u.PromptTokens != 150 || u.CompletionTokens != 37 ||
		u.CacheCreationInputTokens != 20 || u.CacheReadInputTokens != 90 ||
		u.TotalTokens != 187 {
		t.Fatalf("wrong accumulated usage: %+v", u)
	}
	if c.LastStopReason() != "end_turn" {
		t.Fatalf("stop reason = %q", c.LastStopReason())
	}
}

// TestStreamUsageAccumulatorNoEvents: commit without usage events must not
// invent data.
func TestStreamUsageAccumulatorNoEvents(t *testing.T) {
	var acc streamUsageAccumulator
	acc.observe([]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`))

	c := &ClaudeClient{}
	c.resetUsage()
	acc.commit(c)
	if u := c.usage.LastUsage(); u != nil {
		t.Fatalf("usage invented from a stream without usage events: %+v", u)
	}
}

// TestBufferedBodyUsageCapture covers the non-OAuth SendPrompt path's
// read-side parse.
func TestBufferedBodyUsageCapture(t *testing.T) {
	c := &ClaudeClient{}
	c.resetUsage()
	c.recordUsageFromBody([]byte(`{
		"content":[{"type":"text","text":"hi"}],
		"stop_reason":"max_tokens",
		"usage":{"input_tokens":10,"output_tokens":99}
	}`))

	u := c.usage.LastUsage()
	if u == nil || u.PromptTokens != 10 || u.CompletionTokens != 99 || !u.IsReal {
		t.Fatalf("buffered capture wrong: %+v", u)
	}
	if got := c.usage.LastStopReason(); got != "max_tokens" {
		t.Fatalf("stop reason = %q", got)
	}
}
