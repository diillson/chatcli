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

func TestParseClaudeToolResponse_CarriesAppliedContextEdits(t *testing.T) {
	body := `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
	 "context_management":{"applied_edits":[{"type":"clear_tool_uses_20250919","cleared_tool_uses":3,"cleared_input_tokens":25000},{"type":"clear_tool_uses_20250919","cleared_tool_uses":1,"cleared_input_tokens":4000}]},
	 "usage":{"input_tokens":10,"output_tokens":2}}`
	resp, err := parseClaudeToolResponse(body, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if resp.ContextEdits == nil || resp.ContextEdits.ClearedToolUses != 4 || resp.ContextEdits.ClearedInputTokens != 29000 {
		t.Fatalf("edits = %+v", resp.ContextEdits)
	}
	plain, _ := parseClaudeToolResponse(`{"content":[{"type":"text","text":"ok"}]}`, zap.NewNop())
	if plain.ContextEdits != nil {
		t.Fatal("no edits → nil")
	}
}
