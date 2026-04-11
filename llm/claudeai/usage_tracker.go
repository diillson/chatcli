/*
 * ChatCLI - Claude Usage Tracker
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Adds UsageAwareClient and StopReasonAwareClient support to ClaudeClient
 * WITHOUT modifying any existing code in claude_client.go.
 *
 * Strategy: The tool_use.go path already parses usage from SendPromptWithTools.
 * For the main SendPrompt path (OAuth), we extract usage from the response
 * AFTER the original processing is complete, using a separate JSON parse
 * of the already-read response data.
 *
 * IMPORTANT: This file MUST NOT modify any function in claude_client.go.
 * The OAuth flow (headers, message structure, warmup, stream parsing) is
 * extremely sensitive and any change breaks the Anthropic API integration.
 */
package claudeai

import (
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// usageState is stored alongside ClaudeClient to track usage.
// It's accessed by tool_use.go (which already parses usage) and
// can be populated by external callers via RecordUsage/RecordStopReason.
var (
	// globalUsageState tracks usage for the most recent Claude API call.
	// This is a package-level variable because ClaudeClient's struct cannot
	// be modified (OAuth sensitivity). It's safe because Claude calls are
	// serialized (one at a time per client instance).
	globalUsageState client.UsageState
)

// LastUsage returns the token usage from the most recent API call.
// Satisfies the client.UsageAwareClient interface.
func (c *ClaudeClient) LastUsage() *models.UsageInfo {
	return globalUsageState.LastUsage()
}

// LastStopReason returns the stop reason from the most recent API call.
// Satisfies the client.StopReasonAwareClient interface.
func (c *ClaudeClient) LastStopReason() string {
	return globalUsageState.LastStopReason()
}

// RecordClaudeUsage stores usage info from a Claude API response.
// Called by tool_use.go after parsing the response, and can be called
// by any code that has access to usage data from a Claude response.
func RecordClaudeUsage(usage *models.UsageInfo) {
	if usage != nil {
		globalUsageState.StoreUsage(usage)
	}
}

// RecordClaudeStopReason stores the stop reason from a Claude API response.
func RecordClaudeStopReason(reason string) {
	if reason != "" {
		globalUsageState.StoreStopReason(reason)
	}
}
