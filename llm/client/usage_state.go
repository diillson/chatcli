/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"encoding/json"
	"sync"

	"github.com/diillson/chatcli/models"
)

// UsageState is an embeddable struct that provides LastUsage() and LastStopReason()
// implementations. Provider clients embed this to satisfy UsageAwareClient and
// StopReasonAwareClient without duplicating storage and accessor logic.
//
// Usage:
//
//	type MyClient struct {
//	    client.UsageState
//	    // ... other fields
//	}
//
//	// After parsing API response:
//	c.StoreUsage(&models.UsageInfo{...})
//	c.StoreStopReason("end_turn")
type UsageState struct {
	mu        sync.RWMutex
	lastUsage *models.UsageInfo
	lastStop  string
}

// StoreUsage saves usage info from the most recent API response.
// Thread-safe — can be called from retry/goroutine contexts.
func (s *UsageState) StoreUsage(usage *models.UsageInfo) {
	s.mu.Lock()
	s.lastUsage = usage
	s.mu.Unlock()
}

// StoreStopReason saves the stop reason from the most recent API response.
func (s *UsageState) StoreStopReason(reason string) {
	s.mu.Lock()
	s.lastStop = reason
	s.mu.Unlock()
}

// LastUsage returns the token usage from the most recent API call.
// Implements UsageAwareClient.
func (s *UsageState) LastUsage() *models.UsageInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastUsage
}

// LastStopReason returns the stop reason from the most recent API call.
// Implements StopReasonAwareClient.
func (s *UsageState) LastStopReason() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastStop
}

// ParseOpenAIUsage extracts usage info from an OpenAI-compatible Chat
// Completions response map. Works for OpenAI, XAI, Copilot, GitHub Models,
// OpenRouter, ZAI, MiniMax — anything that mirrors the
// `prompt_tokens` / `completion_tokens` / `total_tokens` schema.
//
// Also surfaces:
//   - `prompt_tokens_details.cached_tokens` → CacheReadInputTokens
//     (automatic prompt cache hit count; OpenAI semantics map to
//     Anthropic's cache-read field — both are "input tokens served at a
//     discount because the prefix matched")
//   - `completion_tokens_details.reasoning_tokens` → ReasoningTokens
//     (o-series / GPT-5; already counted inside completion_tokens, this
//     is informational)
func ParseOpenAIUsage(result map[string]interface{}) *models.UsageInfo {
	usage, ok := result["usage"].(map[string]interface{})
	if !ok {
		return nil
	}

	info := &models.UsageInfo{IsReal: true}

	if pt, ok := usage["prompt_tokens"].(float64); ok {
		info.PromptTokens = int(pt)
	}
	if ct, ok := usage["completion_tokens"].(float64); ok {
		info.CompletionTokens = int(ct)
	}
	if tt, ok := usage["total_tokens"].(float64); ok {
		info.TotalTokens = int(tt)
	}
	if details, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
		if cached, ok := details["cached_tokens"].(float64); ok {
			info.CacheReadInputTokens = int(cached)
		}
	}
	if details, ok := usage["completion_tokens_details"].(map[string]interface{}); ok {
		if reasoning, ok := details["reasoning_tokens"].(float64); ok {
			info.ReasoningTokens = int(reasoning)
		}
	}

	// Compute total if not provided
	if info.TotalTokens == 0 && (info.PromptTokens > 0 || info.CompletionTokens > 0) {
		info.TotalTokens = info.PromptTokens + info.CompletionTokens
	}

	return info
}

// openAIResponsesPayload mirrors the subset of OpenAI Responses API payload
// fields the usage parser cares about. Keeping the shape typed (instead of
// walking a generic map) eliminates runtime type assertions on every
// field, narrows the public API surface (no `interface{}` leaking into
// callers), and gives the JSON decoder a single error-reporting path.
type openAIResponsesPayload struct {
	Usage *struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		TotalTokens         int `json:"total_tokens"`
		InputTokensDetails  *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
		OutputTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

// ParseOpenAIResponsesUsage extracts usage info from a raw OpenAI Responses
// API payload (the full /v1/responses body, or the `response` field of a
// `response.completed` SSE event). The Responses schema uses
// `input_tokens` / `output_tokens` instead of `prompt_tokens` /
// `completion_tokens`, and the nested detail objects are renamed
// accordingly — calling ParseOpenAIUsage on a Responses payload silently
// returns zeros, so this is the parser to use here.
//
// Returns:
//   - (nil, nil)  if the payload has no `usage` block (e.g. an
//     incremental streaming event that isn't `response.completed`);
//   - (nil, err)  if the bytes do not parse as JSON;
//   - (info, nil) on success.
//
// Callers typically pass json.RawMessage from the SSE decoder or the raw
// response body — no intermediate map[string]interface{} is needed.
func ParseOpenAIResponsesUsage(data []byte) (*models.UsageInfo, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var payload openAIResponsesPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if payload.Usage == nil {
		return nil, nil
	}
	u := payload.Usage
	info := &models.UsageInfo{
		IsReal:           true,
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.InputTokensDetails != nil {
		info.CacheReadInputTokens = u.InputTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil {
		info.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	if info.TotalTokens == 0 && (info.PromptTokens > 0 || info.CompletionTokens > 0) {
		info.TotalTokens = info.PromptTokens + info.CompletionTokens
	}
	return info, nil
}

// ParseAnthropicUsage extracts usage info from an Anthropic response map.
// Handles both input_tokens/output_tokens naming and cache token fields.
func ParseAnthropicUsage(result map[string]interface{}) *models.UsageInfo {
	usage, ok := result["usage"].(map[string]interface{})
	if !ok {
		return nil
	}

	info := &models.UsageInfo{IsReal: true}

	if it, ok := usage["input_tokens"].(float64); ok {
		info.PromptTokens = int(it)
	}
	if ot, ok := usage["output_tokens"].(float64); ok {
		info.CompletionTokens = int(ot)
	}
	if cc, ok := usage["cache_creation_input_tokens"].(float64); ok {
		info.CacheCreationInputTokens = int(cc)
	}
	if cr, ok := usage["cache_read_input_tokens"].(float64); ok {
		info.CacheReadInputTokens = int(cr)
	}

	info.TotalTokens = info.PromptTokens + info.CompletionTokens
	return info
}

// ParseOpenAIFinishReason extracts the finish_reason from an OpenAI-compatible response.
func ParseOpenAIFinishReason(result map[string]interface{}) string {
	choices, ok := result["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return ""
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return ""
	}
	reason, _ := choice["finish_reason"].(string)
	return reason
}

// ParseAnthropicStopReason extracts the stop_reason from an Anthropic response.
func ParseAnthropicStopReason(result map[string]interface{}) string {
	reason, _ := result["stop_reason"].(string)
	return reason
}
