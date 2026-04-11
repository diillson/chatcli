/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
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

// ParseOpenAIUsage extracts usage info from an OpenAI-compatible response map.
// Works for OpenAI, XAI, Copilot, GitHub Models, OpenRouter, ZAI, MiniMax.
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

	// Compute total if not provided
	if info.TotalTokens == 0 && (info.PromptTokens > 0 || info.CompletionTokens > 0) {
		info.TotalTokens = info.PromptTokens + info.CompletionTokens
	}

	return info
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
