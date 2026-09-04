/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"
	"time"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestCacheTelemetry_SubsetSchemaTotalMissCounts(t *testing.T) {
	c := &cacheTelemetry{}
	now := time.Now()
	// First request: a cache write on OpenAI shows up as cached_tokens=0 —
	// not a miss, there was nothing to hit yet.
	c.observe("OPENAI", "gpt-5.6", &models.UsageInfo{PromptTokens: 5000}, now)
	if c.requests != 0 || c.misses != 0 {
		t.Fatalf("first uncached request is not a miss: %+v", c)
	}
	c.observe("OPENAI", "gpt-5.6", &models.UsageInfo{PromptTokens: 5000, CacheReadInputTokens: 4800}, now)
	// A sizeable prompt served with nothing from cache after a hit: total miss.
	for i := 0; i < cacheMissStreakAlert; i++ {
		c.observe("OPENAI", "gpt-5.6", &models.UsageInfo{PromptTokens: 5000}, now)
	}
	if c.misses != cacheMissStreakAlert || !c.alertArmed {
		t.Fatalf("total misses must count and arm the streak alert: misses=%d armed=%v", c.misses, c.alertArmed)
	}
	// A tiny prompt below the provider's cacheable minimum is never a miss.
	before := c.misses
	c.observe("OPENAI", "gpt-5.6", &models.UsageInfo{PromptTokens: 200}, now)
	if c.misses != before {
		t.Fatal("prompts under the cacheable minimum are not misses")
	}
	// Additive schema with no marker reported: still nothing to learn.
	c.observe("CLAUDEAI", "claude-sonnet-5", &models.UsageInfo{PromptTokens: 9000}, now)
	if c.misses != before {
		t.Fatal("no marker on an additive schema is not a miss")
	}
}

func TestCacheTelemetry_FirstAndHitRatioArePerProvider(t *testing.T) {
	ct := NewCostTracker()
	now := time.Now()
	// Anthropic: one write then reads → high hit ratio.
	ct.cache.observe("CLAUDEAI", "claude-sonnet-5", &models.UsageInfo{PromptTokens: 100, CacheCreationInputTokens: 9000}, now)
	ct.cache.observe("CLAUDEAI", "claude-sonnet-5", &models.UsageInfo{PromptTokens: 100, CacheReadInputTokens: 9000}, now)
	// Switching to OpenAI: its first request must not be counted as a miss
	// (the Anthropic "first" was already spent), and its ratio is its own.
	ct.cache.observe("OPENAI", "gpt-5.6", &models.UsageInfo{PromptTokens: 5000}, now)
	if ct.cache.misses != 0 {
		t.Fatalf("the first OpenAI request is not a miss: %+v", ct.cache)
	}
	ct.cache.observe("OPENAI", "gpt-5.6", &models.UsageInfo{PromptTokens: 5000, CacheReadInputTokens: 1000}, now)
	stats := ct.CacheStats()
	// OpenAI bucket (the uncached first request teaches nothing and is not
	// counted): 1000 cached of 5000 prompt tokens → 20%, not the
	// Anthropic-inflated blend.
	if stats.HitPct < 19 || stats.HitPct > 21 {
		t.Fatalf("hit ratio must be the last provider's own: %.1f", stats.HitPct)
	}
}

func TestCacheTTLFor_BedrockReportsTheMarkerActuallySent(t *testing.T) {
	t.Setenv(llmclient.PromptCacheTTLEnv, "1h")
	if got := cacheTTLFor("BEDROCK", "anthropic.claude-sonnet-5"); got != "1h" {
		t.Fatalf("Claude 4.5+ on Bedrock carries the 1h marker: %s", got)
	}
	if got := cacheTTLFor("BEDROCK", "anthropic.claude-3-7-sonnet-20250219-v1:0"); got != "5m" {
		t.Fatalf("older Claude keeps 5m: %s", got)
	}
	t.Setenv(llmclient.PromptCacheTTLEnv, "5m")
	if got := cacheTTLFor("BEDROCK", "anthropic.claude-sonnet-5"); got != "5m" {
		t.Fatalf("default lifetime: %s", got)
	}
}

func TestNotePrefixChanged_MarksTheNextWriteAsRebuild(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop(), costTracker: NewCostTracker()}
	c.notePrefixChanged("switch model")
	if !c.costTracker.cache.rebuildPending {
		t.Fatal("prefix change must arm the expected-rebuild flag")
	}
	var nilCLI *ChatCLI
	nilCLI.notePrefixChanged("noop") // must not panic
	(&ChatCLI{}).notePrefixChanged("no tracker")
}
