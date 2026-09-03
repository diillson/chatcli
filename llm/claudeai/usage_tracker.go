/*
 * ChatCLI - Claude Usage Tracker
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Adds UsageAwareClient and StopReasonAwareClient support to ClaudeClient.
 *
 * Usage is captured READ-SIDE ONLY: from the already-received response body
 * (buffered path) or from the already-decoded SSE events (OAuth stream
 * path), never by changing how requests are built — the OAuth flow
 * (headers, message structure, warmup) is extremely sensitive and stays
 * byte-identical.
 *
 * State is per-client-instance (ClaudeClient.usage), so parallel clients
 * (MoA panels, workers) never cross-attribute tokens. The legacy package
 * global is kept as a fallback for the exported Record* helpers.
 */
package claudeai

import (
	"encoding/json"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

var (
	// globalUsageState is the legacy package-level sink fed by the exported
	// Record* helpers. WRITE-ONLY from the client's perspective: LastUsage /
	// LastStopReason never read it (cross-instance reads double-count), it
	// exists solely so external Record* callers keep compiling.
	globalUsageState client.UsageState
)

// LastUsage returns the token usage from the most recent API call.
// Satisfies the client.UsageAwareClient interface.
//
// Instance state ONLY — deliberately no globalUsageState fallback. Every
// send path stores per-instance (SendPrompt buffered + OAuth stream,
// SendPromptWithTools), so a nil here genuinely means "this call reported
// nothing" and the caller falls back to estimation. Falling through to the
// global would re-attribute another client's tokens (e.g. a worker reading
// the main loop's 120K-token turn) and double-count them.
func (c *ClaudeClient) LastUsage() *models.UsageInfo {
	return c.usage.LastUsage()
}

// LastStopReason returns the stop reason from the most recent API call.
// Satisfies the client.StopReasonAwareClient interface. Instance-only, same
// rationale as LastUsage.
func (c *ClaudeClient) LastStopReason() string {
	return c.usage.LastStopReason()
}

// resetUsage clears this instance's usage at the start of a call, so a call
// that yields no usage (error, schema change) reads as "unknown" and falls
// back to estimation — never as a stale re-count of the previous call.
func (c *ClaudeClient) resetUsage() {
	c.usage.StoreUsage(nil)
	c.usage.StoreStopReason("")
}

// storeUsage records usage on this instance (and mirrors to the legacy
// global so existing consumers of the package-level state keep working).
func (c *ClaudeClient) storeUsage(usage *models.UsageInfo) {
	if usage == nil {
		return
	}
	c.usage.StoreUsage(usage)
	globalUsageState.StoreUsage(usage)
}

// recordUsageFromBody extracts usage from a buffered Anthropic Messages
// response body with a SEPARATE parse of the already-read bytes.
func (c *ClaudeClient) recordUsageFromBody(body []byte) {
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return
	}
	c.storeUsage(client.ParseAnthropicUsage(result))
	if reason := client.ParseAnthropicStopReason(result); reason != "" {
		c.usage.StoreStopReason(reason)
		globalUsageState.StoreStopReason(reason)
	}
}

// streamUsageAccumulator collects usage across the SSE events of one
// streamed Anthropic response: message_start carries input/cache counts
// (and an initial output count), message_delta carries the final output
// count and stop reason.
type streamUsageAccumulator struct {
	info       models.UsageInfo
	stopReason string
	seen       bool
}

// anthropicStreamEvent mirrors the usage-bearing subset of Anthropic SSE
// events. Both message_start (nested under "message") and message_delta
// (top-level "usage" + "delta.stop_reason") shapes are covered.
type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Message *struct {
		Usage *anthropicStreamUsage `json:"usage"`
	} `json:"message"`
	Usage *anthropicStreamUsage `json:"usage"`
	Delta *struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
}

type anthropicStreamUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreation            *struct {
		Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// observe folds one SSE data payload into the accumulator. Cheap no-op for
// content deltas (the JSON parse of small events is negligible next to the
// network stream itself).
func (a *streamUsageAccumulator) observe(data []byte) {
	var evt anthropicStreamEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return
	}
	switch evt.Type {
	case "message_start":
		if evt.Message != nil && evt.Message.Usage != nil {
			u := evt.Message.Usage
			a.info.PromptTokens = u.InputTokens
			a.info.CompletionTokens = u.OutputTokens
			a.info.CacheCreationInputTokens = u.CacheCreationInputTokens
			a.info.CacheReadInputTokens = u.CacheReadInputTokens
			if u.CacheCreation != nil {
				a.info.CacheCreation1hInputTokens = u.CacheCreation.Ephemeral1h
			}
			a.seen = true
		}
	case "message_delta":
		if evt.Usage != nil && evt.Usage.OutputTokens > 0 {
			a.info.CompletionTokens = evt.Usage.OutputTokens
			a.seen = true
		}
		if evt.Delta != nil && evt.Delta.StopReason != "" {
			a.stopReason = evt.Delta.StopReason
		}
	}
}

// commit stores the accumulated usage on the client once the stream ends.
func (a *streamUsageAccumulator) commit(c *ClaudeClient) {
	if !a.seen {
		return
	}
	info := a.info
	info.TotalTokens = info.PromptTokens + info.CompletionTokens
	info.IsReal = true
	c.storeUsage(&info)
	if a.stopReason != "" {
		c.usage.StoreStopReason(a.stopReason)
		globalUsageState.StoreStopReason(a.stopReason)
	}
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
