package models

import "encoding/json"

// ToolDefinition describes a tool the LLM can call via native API.
type ToolDefinition struct {
	Type     string          `json:"type"` // "function"
	Function ToolFunctionDef `json:"function"`
}

// ToolFunctionDef is the function schema within a tool definition.
type ToolFunctionDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCall represents a tool invocation from the LLM response.
type ToolCall struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"` // "function"
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
	Raw       string                 `json:"raw,omitempty"` // Original text if parsed from XML
}

// ArgumentsJSON returns the arguments as a JSON string.
func (tc ToolCall) ArgumentsJSON() string {
	if len(tc.Arguments) == 0 {
		return "{}"
	}
	b, err := json.Marshal(tc.Arguments)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ToolResult is sent back after executing a tool.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// ContentBlock supports multi-part content (text + tool_use).
type ContentBlock struct {
	Type         string        `json:"type"` // "text", "tool_use", "tool_result"
	Text         string        `json:"text,omitempty"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// CacheControl for Anthropic KV cache optimization.
type CacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// LLMResponse is the structured response from tool-aware providers.
type LLMResponse struct {
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Usage      *UsageInfo `json:"usage,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"`
}

// HasToolCalls returns true if the response contains tool calls.
func (r *LLMResponse) HasToolCalls() bool {
	return len(r.ToolCalls) > 0
}
