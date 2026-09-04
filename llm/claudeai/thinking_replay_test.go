package claudeai

import (
	"encoding/json"
	"testing"

	"github.com/diillson/chatcli/models"
)

// The tool path is where the contract bites: with extended thinking on,
// the assistant turn that carries a tool_use must carry back the thinking
// that produced it.
func TestParseClaudeToolResponseKeepsThinking(t *testing.T) {
	body := `{
	  "stop_reason":"tool_use",
	  "content":[
	    {"type":"thinking","thinking":"weighing options","signature":"sig-abc"},
	    {"type":"text","text":"running it"},
	    {"type":"tool_use","id":"tu_1","name":"read","input":{"path":"a.go"}}
	  ]
	}`
	resp, err := parseClaudeToolResponse(body, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(resp.Thinking) != 1 || resp.Thinking[0].Signature != "sig-abc" {
		t.Fatalf("thinking not captured: %+v", resp.Thinking)
	}
	if resp.Content != "running it" {
		t.Errorf("text block changed: %q", resp.Content)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "tu_1" {
		t.Errorf("tool call changed: %+v", resp.ToolCalls)
	}
}

func TestBuildClaudeToolMessagesReplaysThinkingFirst(t *testing.T) {
	history := []models.Message{
		{Role: "user", Content: "go"},
		{
			Role:      "assistant",
			Content:   "running it",
			Thinking:  []models.ThinkingBlock{{Type: "thinking", Thinking: "weighing", Signature: "sig-abc"}},
			ToolCalls: []models.ToolCall{{ID: "tu_1", Name: "read", Arguments: map[string]interface{}{"path": "a.go"}}},
		},
		{Role: "tool", ToolCallID: "tu_1", Content: "package main"},
	}
	raw, err := json.Marshal(buildClaudeToolMessages("", history))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// User turns marshal their content as a plain string, assistant turns
	// with tool calls as a block array — decode lazily so both fit.
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msgs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var assistant []map[string]any
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		if err := json.Unmarshal(m.Content, &assistant); err != nil {
			t.Fatalf("assistant content is not a block array: %v", err)
		}
	}
	if len(assistant) != 3 {
		t.Fatalf("want thinking+text+tool_use, got %d blocks: %+v", len(assistant), assistant)
	}
	if assistant[0]["type"] != "thinking" || assistant[0]["signature"] != "sig-abc" {
		t.Errorf("thinking must lead the assistant turn, got %+v", assistant[0])
	}
	if assistant[1]["type"] != "text" || assistant[2]["type"] != "tool_use" {
		t.Errorf("block order after thinking changed: %+v", assistant[1:])
	}
}
