/*
 * ChatCLI - Bedrock usage capture
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Read-side token accounting for every Bedrock surface. All four request
 * families report real usage — InvokeModel (Anthropic Messages body),
 * Converse (typed TokenUsage), the OpenAI-compatible family and the Mantle
 * endpoint — and this file folds each shape into client.UsageState so
 * BedrockClient satisfies UsageAwareClient/StopReasonAwareClient. Without
 * it, Bedrock costs were chars/4 estimates dressed up with real prices.
 */
package bedrock

import (
	"encoding/json"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// LastUsage returns the token usage from the most recent API call.
// Satisfies the client.UsageAwareClient interface.
func (c *BedrockClient) LastUsage() *models.UsageInfo {
	return c.usage.LastUsage()
}

// LastStopReason returns the stop reason from the most recent API call.
// Satisfies the client.StopReasonAwareClient interface.
func (c *BedrockClient) LastStopReason() string {
	return c.usage.LastStopReason()
}

// resetUsage clears the state at the start of a call so an errored or
// usage-less response falls back to estimation instead of re-counting the
// previous call.
func (c *BedrockClient) resetUsage() {
	c.usage.StoreUsage(nil)
	c.usage.StoreStopReason("")
}

// captureAnthropicUsage parses the usage block of an Anthropic Messages
// response body (InvokeModel and Mantle share the envelope). Separate parse
// of already-read bytes — the text path stays untouched.
func (c *BedrockClient) captureAnthropicUsage(body []byte) {
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return
	}
	if info := client.ParseAnthropicUsage(result); info != nil {
		c.usage.StoreUsage(info)
	}
	if reason := client.ParseAnthropicStopReason(result); reason != "" {
		c.usage.StoreStopReason(reason)
	}
}

// captureConverseUsage folds the Converse API's typed TokenUsage into the
// client state. Cache fields follow Anthropic semantics on Bedrock
// (reported alongside InputTokens, not as a subset).
func (c *BedrockClient) captureConverseUsage(out *bedrockruntime.ConverseOutput) {
	if out == nil || out.Usage == nil {
		return
	}
	deref := func(v *int32) int {
		if v == nil {
			return 0
		}
		return int(*v)
	}
	info := &models.UsageInfo{
		PromptTokens:             deref(out.Usage.InputTokens),
		CompletionTokens:         deref(out.Usage.OutputTokens),
		TotalTokens:              deref(out.Usage.TotalTokens),
		CacheReadInputTokens:     deref(out.Usage.CacheReadInputTokens),
		CacheCreationInputTokens: deref(out.Usage.CacheWriteInputTokens),
		IsReal:                   true,
	}
	if info.TotalTokens == 0 {
		info.TotalTokens = info.PromptTokens + info.CompletionTokens
	}
	c.usage.StoreUsage(info)
	if out.StopReason != "" {
		c.usage.StoreStopReason(string(out.StopReason))
	}
}

// captureOpenAIUsage parses the usage block of an OpenAI-compatible
// InvokeModel response body (gpt-oss family).
func (c *BedrockClient) captureOpenAIUsage(body []byte) {
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return
	}
	if info := client.ParseOpenAIUsage(result); info != nil {
		c.usage.StoreUsage(info)
	}
	if reason := client.ParseOpenAIFinishReason(result); reason != "" {
		c.usage.StoreStopReason(reason)
	}
}
