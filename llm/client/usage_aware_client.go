/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import "github.com/diillson/chatcli/models"

// UsageAwareClient is an optional interface that LLM clients implement
// to expose real token usage data from API responses.
//
// Providers that implement this interface return actual token counts
// as reported by the API, enabling accurate cost tracking. Providers
// that don't implement this interface fall back to character-based estimation.
//
// The usage data is populated after each SendPrompt or SendPromptWithTools call.
// Callers should retrieve it before the next call, as it will be overwritten.
type UsageAwareClient interface {
	LLMClient

	// LastUsage returns the token usage from the most recent API call.
	// Returns nil if usage data was not available in the response.
	LastUsage() *models.UsageInfo
}

// StopReasonAwareClient is an optional interface that LLM clients implement
// to expose the stop reason from the most recent API response.
//
// This enables the agent loop to detect:
//   - "max_tokens": response was cut off, may need escalation
//   - "end_turn" / "stop": normal completion
//   - "tool_use": model wants to call tools
type StopReasonAwareClient interface {
	LLMClient

	// LastStopReason returns the stop reason from the most recent API call.
	// Common values: "end_turn", "max_tokens", "stop_sequence", "tool_use".
	// Returns empty string if not available.
	LastStopReason() string
}

// IsUsageAware checks if a client reports real token usage.
func IsUsageAware(c LLMClient) bool {
	_, ok := c.(UsageAwareClient)
	return ok
}

// AsUsageAware casts a client to UsageAwareClient if supported.
func AsUsageAware(c LLMClient) (UsageAwareClient, bool) {
	uac, ok := c.(UsageAwareClient)
	return uac, ok
}

// IsStopReasonAware checks if a client reports stop reasons.
func IsStopReasonAware(c LLMClient) bool {
	_, ok := c.(StopReasonAwareClient)
	return ok
}

// AsStopReasonAware casts a client to StopReasonAwareClient if supported.
func AsStopReasonAware(c LLMClient) (StopReasonAwareClient, bool) {
	src, ok := c.(StopReasonAwareClient)
	return src, ok
}

// GetUsageOrEstimate retrieves real usage from a UsageAwareClient,
// or falls back to character-based estimation.
func GetUsageOrEstimate(c LLMClient, inputChars, outputChars int) *models.UsageInfo {
	if uac, ok := AsUsageAware(c); ok {
		if usage := uac.LastUsage(); usage != nil {
			return usage
		}
	}
	return models.EstimateFromChars(inputChars, outputChars)
}
