/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"testing"

	"go.uber.org/zap"
)

func TestParseClaudeToolResponse_CarriesTheHourCacheWriteShare(t *testing.T) {
	body := `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":500,"cache_read_input_tokens":300,"cache_creation":{"ephemeral_1h_input_tokens":500,"ephemeral_5m_input_tokens":0}}}`
	resp, err := parseClaudeToolResponse(body, zap.NewNop())
	if err != nil || resp == nil || resp.Usage == nil {
		t.Fatalf("parse: %v %+v", err, resp)
	}
	u := resp.Usage
	if u.PromptTokens != 10 || u.CompletionTokens != 2 || u.CacheCreationInputTokens != 500 || u.CacheReadInputTokens != 300 || !u.IsReal {
		t.Fatalf("usage = %+v", u)
	}
	if u.CacheCreation1hInputTokens != 500 {
		t.Fatalf("the 1h write share must reach the cost tracker (2x rate), got %d", u.CacheCreation1hInputTokens)
	}
}
