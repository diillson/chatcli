package models

import "github.com/diillson/chatcli/config"

// MessageMeta carries non-content metadata for history management.
type MessageMeta struct {
	IsSummary bool   `json:"is_summary,omitempty"` // true if this message is a compacted summary
	SummaryOf int    `json:"summary_of,omitempty"` // how many original messages were summarized
	Mode      string `json:"mode,omitempty"`       // "chat", "agent", "coder" — which mode produced this message
}

// Message representa uma mensagem trocada com o modelo de linguagem.
type Message struct {
	Role        string         `json:"role"`                   // O papel da mensagem: "user", "assistant", "system", "tool".
	Content     string         `json:"content"`                // O conteúdo da mensagem.
	Meta        *MessageMeta   `json:"meta,omitempty"`         // Optional metadata for history compaction.
	ToolCalls   []ToolCall     `json:"tool_calls,omitempty"`   // Tool calls from assistant (native API).
	ToolCallID  string         `json:"tool_call_id,omitempty"` // ID when this message is a tool result.
	SystemParts []ContentBlock `json:"system_parts,omitempty"` // Structured system prompt parts (for cache control).

	// Images carries vision input attached to this turn. Populated for
	// user messages (an attached/pasted/forwarded image) and consumed by
	// vision-capable provider adapters, which serialize each entry into
	// their native image block. Providers without vision ignore it (the
	// gateway/CLI may instead route through the describe-fallback). The
	// text in Content still applies — an image usually rides with a caption.
	Images []ImageContent `json:"images,omitempty"`

	// IsError marks this tool-result message as a business-level
	// failure (the tool ran, but reported an error: command exit code,
	// HTTP 4xx, missing file). Provider adapters use it to set the
	// native is_error wire field (Anthropic) or prefix the content
	// with a marker the model can read (OpenAI-family).
	//
	// Only meaningful when Role == "tool". Default false (success).
	IsError bool `json:"is_error,omitempty"`

	// ErrorCode is the stable, locale-independent classification of
	// the failure (ENOENT, EACCES, Timeout, ExitCode:2, NetworkError,
	// etc). Surfaced to the LLM so it can reason about retryability
	// without parsing English. Empty when IsError is false; carried
	// inside the content marker for providers without native support.
	ErrorCode string `json:"error_code,omitempty"`
}

// NewToolResultMessage builds a properly-shaped tool-result message
// from the agent layer. Centralizing the construction here keeps every
// caller in lock-step: ToolCallID always set, role always "tool",
// IsError/ErrorCode coherent with whatever the executor reported.
func NewToolResultMessage(toolCallID, content string, isError bool, errorCode string) Message {
	return Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: toolCallID,
		IsError:    isError,
		ErrorCode:  errorCode,
	}
}

// IsValid valida se a mensagem tem um papel e conteúdo válidos.
func (m *Message) IsValid() bool {
	validRoles := map[string]bool{
		"user":      true,
		"assistant": true,
		"system":    true,
		"tool":      true,
	}
	// Tool result messages may have empty content if they carry errors
	if m.Role == "tool" {
		return m.ToolCallID != ""
	}
	// Assistant messages with tool calls may have empty content
	if m.Role == "assistant" && len(m.ToolCalls) > 0 {
		return true
	}
	// A turn carrying an image (with or without a text caption) is valid.
	return validRoles[m.Role] && (m.Content != "" || len(m.Images) > 0)
}

// ResponseData representa os dados de resposta da LLM.
type ResponseData struct {
	Status   string `json:"status"`   // O status da resposta: "processing", "completed", ou "error".
	Response string `json:"response"` // A resposta da LLM, se o status for "completed".
	Message  string `json:"message"`  // Mensagem de erro, se o status for "error".
}

// IsValid valida se o status da resposta é um dos valores esperados.
func (r *ResponseData) IsValid() bool {
	validStatuses := map[string]bool{
		config.StatusProcessing: true,
		config.StatusCompleted:  true,
		config.StatusError:      true,
	}
	return validStatuses[r.Status]
}

// SessionData is the v2 session format that supports scoped histories.
// It is backward-compatible with the legacy format (plain []Message).
type SessionData struct {
	Version      int       `json:"version"` // 2 for the new format
	ChatHistory  []Message `json:"chat_history"`
	AgentHistory []Message `json:"agent_history,omitempty"`
	CoderHistory []Message `json:"coder_history,omitempty"`
	SharedMemory []Message `json:"shared_memory,omitempty"`
}

// UsageInfo represents token usage information returned by LLM APIs.
// All fields are optional — providers populate what they report.
type UsageInfo struct {
	// Core token counts (reported by all providers)
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// Anthropic prompt caching (reduces cost for repeated prefixes).
	// OpenAI cached prompt tokens are also reported here under
	// CacheReadInputTokens — semantically the same thing (repeated input
	// served at a discount). CacheCreationInputTokens stays Anthropic-only.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`

	// Reasoning tokens emitted by o-series / GPT-5 reasoning models.
	// Reported by OpenAI under usage.completion_tokens_details.reasoning_tokens
	// (Chat Completions) or usage.output_tokens_details.reasoning_tokens
	// (Responses API). Billed as output tokens and already counted in
	// CompletionTokens — this field is informational only.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`

	// Whether these values came from the API (true) or were estimated (false).
	// Callers can use this to decide display precision and cost accuracy.
	IsReal bool `json:"-"`
}

// Merge adds the token counts from other into this UsageInfo.
// Useful for aggregating usage across multiple API calls in a session.
func (u *UsageInfo) Merge(other *UsageInfo) {
	if other == nil {
		return
	}
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.CacheCreationInputTokens += other.CacheCreationInputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
	u.ReasoningTokens += other.ReasoningTokens
	if other.IsReal {
		u.IsReal = true
	}
}

// EstimateFromChars creates a UsageInfo estimated from character counts.
// Uses 4 chars per token heuristic. Marked as IsReal=false.
func EstimateFromChars(inputChars, outputChars int) *UsageInfo {
	prompt := inputChars / 4
	completion := outputChars / 4
	return &UsageInfo{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      prompt + completion,
		IsReal:           false,
	}
}
