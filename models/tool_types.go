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
	// TTL is the optional cache lifetime ("5m" default, "1h" extended).
	// Empty means the provider default.
	TTL string `json:"ttl,omitempty"`
}

// LLMResponse is the structured response from tool-aware providers.
type LLMResponse struct {
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Usage      *UsageInfo `json:"usage,omitempty"`
	StopReason string     `json:"stop_reason,omitempty"`
	// ContextEdits reports what the provider's context engine removed
	// from the request server-side (Anthropic context editing); nil when
	// nothing was edited.
	ContextEdits *ContextEdits `json:"context_edits,omitempty"`
	// Thinking carries the provider-native reasoning blocks of this
	// response, in the order they arrived. They must be replayed verbatim
	// inside the assistant turn on the next request; empty when the
	// response carried none.
	Thinking []ThinkingBlock `json:"thinking,omitempty"`
}

// ThinkingBlock is one provider-native reasoning block. It is opaque: the
// text of a "thinking" block travels with a signature the provider
// verifies, and a "redacted_thinking" block carries an encrypted payload
// with no readable text. Neither may be edited, summarized or re-ordered —
// they are replayed exactly as received or dropped entirely.
type ThinkingBlock struct {
	Type      string `json:"type"`                // "thinking" or "redacted_thinking"
	Thinking  string `json:"thinking,omitempty"`  // reasoning text ("thinking" only)
	Signature string `json:"signature,omitempty"` // provider signature ("thinking" only)
	Data      string `json:"data,omitempty"`      // encrypted payload ("redacted_thinking" only)
}

// ContextEdits sums the server-side edits a provider applied to one
// request: how many tool results it cleared and how many input tokens
// that freed. The local history still holds those results until the
// caller stubs them (cli.mirrorContextEdits).
type ContextEdits struct {
	ClearedToolUses    int `json:"cleared_tool_uses"`
	ClearedInputTokens int `json:"cleared_input_tokens"`
}

// HasToolCalls returns true if the response contains tool calls.
func (r *LLMResponse) HasToolCalls() bool {
	return len(r.ToolCalls) > 0
}
