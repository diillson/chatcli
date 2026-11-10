/*
 * ChatCLI - Tests for conversation-bound reasoning blocks on the Claude API
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Fable 5.2 refuses a replayed thinking block whose preceding conversation
// changed, on every account (platform.claude.com/docs/en/models/fable-5-2/
// migration-guide). The tool path replays the blocks, so it must drop the
// refused ones, resend once, and keep leaving them out afterwards.
func TestSendPromptWithTools_DropsThinkingBoundToAnotherConversation(t *testing.T) {
	const rejection = `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block. The block is bound to a different conversation. Remove the block, or set ` + "`thinking.block_binding.prefix_mismatch_behavior`" + ` to \"drop_block\"."}}`
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(raw), `"signature":"sig-stale"`) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(rejection))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"fresh","signature":"sig-fresh"},{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`))
	}))
	defer server.Close()

	c := NewClaudeClient(auth.NewStaticTokenProvider("sk-test", auth.AuthModeAPIKey, auth.ProviderAnthropic), "claude-fable-5-2", zap.NewNop(), 3, 0)
	c.apiURL = server.URL
	tools := []models.ToolDefinition{{Type: "function", Function: models.ToolFunctionDef{Name: "read_file", Description: "read", Parameters: map[string]interface{}{"type": "object"}}}}
	history := []models.Message{
		{Role: "user", Content: "go"},
		{
			Role:      "assistant",
			Thinking:  []models.ThinkingBlock{{Type: "thinking", Thinking: "old", Signature: "sig-stale"}},
			ToolCalls: []models.ToolCall{{ID: "t1", Type: "function", Name: "read_file", Arguments: map[string]interface{}{}}},
		},
		{Role: "tool", ToolCallID: "t1", Content: "data"},
	}

	resp, err := c.SendPromptWithTools(context.Background(), "", history, tools, 100)
	if err != nil || resp == nil || resp.Content != "ok" {
		t.Fatalf("the request must recover: resp=%+v err=%v", resp, err)
	}
	if len(bodies) != 2 {
		t.Fatalf("want the refused request and one resend, got %d requests", len(bodies))
	}
	if strings.Contains(bodies[1], `"thinking"`) && strings.Contains(bodies[1], "sig-stale") {
		t.Fatal("the resend must not carry the refused block")
	}
	if !strings.Contains(bodies[1], `"tool_use"`) {
		t.Fatal("the resend must keep the rest of the assistant turn")
	}
	if len(history[1].Thinking) != 1 {
		t.Fatal("the caller's history must not be modified")
	}
	if len(resp.Thinking) != 1 || resp.Thinking[0].Signature != "sig-fresh" {
		t.Fatalf("the new reasoning block must come back: %+v", resp.Thinking)
	}

	// Next turn: the stale block is left out up front (no second 400), the
	// block produced after the refusal travels.
	history = append(history, models.Message{
		Role:      "assistant",
		Thinking:  resp.Thinking,
		ToolCalls: []models.ToolCall{{ID: "t2", Type: "function", Name: "read_file", Arguments: map[string]interface{}{}}},
	}, models.Message{Role: "tool", ToolCallID: "t2", Content: "more"})
	bodies = nil
	if _, err := c.SendPromptWithTools(context.Background(), "", history, tools, 100); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if len(bodies) != 1 || strings.Contains(bodies[0], "sig-stale") || !strings.Contains(bodies[0], "sig-fresh") {
		t.Fatalf("second turn must send once, without the stale block and with the fresh one: %d requests", len(bodies))
	}
}
