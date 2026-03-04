package models

import (
	"encoding/json"
	"testing"
)

func TestToolCall_ArgumentsJSON(t *testing.T) {
	tc := ToolCall{
		ID:   "call_123",
		Name: "test_tool",
		Arguments: map[string]interface{}{
			"key": "value",
		},
	}

	argsJSON := tc.ArgumentsJSON()
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		t.Fatalf("ArgumentsJSON returned invalid JSON: %v", err)
	}
	if parsed["key"] != "value" {
		t.Errorf("expected key=value, got %v", parsed["key"])
	}
}

func TestToolCall_ArgumentsJSON_Empty(t *testing.T) {
	tc := ToolCall{}
	if got := tc.ArgumentsJSON(); got != "{}" {
		t.Errorf("expected '{}', got %q", got)
	}
}

func TestLLMResponse_HasToolCalls(t *testing.T) {
	r := &LLMResponse{}
	if r.HasToolCalls() {
		t.Error("expected false with no tool calls")
	}

	r.ToolCalls = []ToolCall{{ID: "1", Name: "test"}}
	if !r.HasToolCalls() {
		t.Error("expected true with tool calls")
	}
}

func TestToolDefinition_Serialization(t *testing.T) {
	td := ToolDefinition{
		Type: "function",
		Function: ToolFunctionDef{
			Name:        "read_file",
			Description: "Reads a file",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "File path",
					},
				},
				"required": []interface{}{"path"},
			},
		},
	}

	data, err := json.Marshal(td)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed ToolDefinition
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed.Function.Name != "read_file" {
		t.Errorf("expected 'read_file', got %q", parsed.Function.Name)
	}
}

func TestMessage_WithToolCalls_Serialization(t *testing.T) {
	msg := Message{
		Role:    "assistant",
		Content: "Let me check that file.",
		ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Name: "read_file",
				Arguments: map[string]interface{}{"path": "/tmp/test.go"}},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed Message
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(parsed.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(parsed.ToolCalls))
	}
	if parsed.ToolCalls[0].Name != "read_file" {
		t.Errorf("expected 'read_file', got %q", parsed.ToolCalls[0].Name)
	}
}

func TestMessage_ToolResult_IsValid(t *testing.T) {
	msg := Message{
		Role:       "tool",
		ToolCallID: "call_1",
		Content:    "file contents here",
	}
	if !msg.IsValid() {
		t.Error("tool result with ToolCallID should be valid")
	}

	msg2 := Message{Role: "tool", Content: "no id"}
	if msg2.IsValid() {
		t.Error("tool result without ToolCallID should be invalid")
	}
}

func TestMessage_AssistantWithToolCalls_IsValid(t *testing.T) {
	msg := Message{
		Role:      "assistant",
		ToolCalls: []ToolCall{{ID: "1", Name: "test"}},
	}
	if !msg.IsValid() {
		t.Error("assistant with tool calls and no content should be valid")
	}
}

func TestContentBlock_CacheControl(t *testing.T) {
	cb := ContentBlock{
		Type:         "text",
		Text:         "system prompt",
		CacheControl: &CacheControl{Type: "ephemeral"},
	}

	data, err := json.Marshal(cb)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var parsed ContentBlock
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if parsed.CacheControl == nil {
		t.Fatal("expected cache_control to be preserved")
	}
	if parsed.CacheControl.Type != "ephemeral" {
		t.Errorf("expected 'ephemeral', got %q", parsed.CacheControl.Type)
	}
}
