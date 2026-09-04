/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"math"
	"testing"
	"time"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

func realUsage(prompt, read, write int) *models.UsageInfo {
	return &models.UsageInfo{PromptTokens: prompt, CompletionTokens: 10, CacheReadInputTokens: read, CacheCreationInputTokens: write, IsReal: true}
}

func TestCacheTelemetry_AdditiveSchemaHitsAndMisses(t *testing.T) {
	ct := NewCostTracker()
	// Turn 1: cold start — a big write is NOT a miss.
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(500, 0, 20000))
	// Turns 2-3: prefix served from cache, small tail written.
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20000, 400))
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20400, 350))
	stats := ct.CacheStats()
	if stats.Requests != 3 || stats.Misses != 0 {
		t.Fatalf("stats = %+v, want 3 requests and no misses", stats)
	}
	if stats.HitPct < 60 {
		t.Fatalf("hit pct = %.1f, want the bulk of input from cache", stats.HitPct)
	}
	if !stats.Warm || stats.TTL != "5m" {
		t.Fatalf("expected warm 5m cache, got %+v", stats)
	}

	// Turn 4: the prefix changed — large rewrite = miss.
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 1000, 21000))
	if s := ct.CacheStats(); s.Misses != 1 {
		t.Fatalf("expected 1 miss, got %+v", s)
	}
}

func TestCacheTelemetry_ExpectedRebuildIsNotAMiss(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(500, 0, 20000))
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 20000, 400))
	ct.NoteExpectedCacheRebuild() // compaction rewrote the history
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 0, 9000))
	s := ct.CacheStats()
	if s.Misses != 0 || s.Rebuilds != 1 {
		t.Fatalf("rewrite after compaction must count as rebuild, got %+v", s)
	}
	if ct.TakeCacheMissAlert() {
		t.Fatal("no alert expected after an explained rebuild")
	}
}

func TestCacheTelemetry_MissStreakArmsOneShotAlert(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(500, 0, 20000))
	for i := 0; i < cacheMissStreakAlert; i++ {
		ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(300, 500, 15000))
	}
	if !ct.TakeCacheMissAlert() {
		t.Fatal("alert must arm after the streak threshold")
	}
	if ct.TakeCacheMissAlert() {
		t.Fatal("alert is one-shot")
	}
	if s := ct.CacheStats(); s.Misses != cacheMissStreakAlert {
		t.Fatalf("misses = %d", s.Misses)
	}
}

func TestCacheTelemetry_SubsetSchema(t *testing.T) {
	ct := NewCostTracker()
	// OpenAI reports cached tokens as a subset of prompt_tokens.
	ct.RecordRealUsage("OPENAI", "gpt-5.6", realUsage(8000, 0, 0))
	if ct.CacheStats().Reported() {
		t.Fatal("no cache fields reported yet")
	}
	ct.RecordRealUsage("OPENAI", "gpt-5.6", realUsage(8200, 7900, 0))
	s := ct.CacheStats()
	if !s.Reported() || s.HitPct < 90 {
		t.Fatalf("subset hit pct = %+v", s)
	}
	ct.RecordRealUsage("OPENAI", "gpt-5.6", realUsage(8300, 100, 0))
	// 100 of 8300 is below the share floor → miss; but a request with NO
	// cache fields at all is simply not observed.
	if s := ct.CacheStats(); s.Misses != 1 {
		t.Fatalf("expected 1 subset miss, got %+v", s)
	}
}

func TestTurnCacheHitPct(t *testing.T) {
	if _, ok := TurnCacheHitPct("OPENAI", "gpt-5.6", realUsage(100, 0, 0)); ok {
		t.Fatal("no cache fields → not reported")
	}
	pct, ok := TurnCacheHitPct("CLAUDEAI", "claude-sonnet-5", realUsage(1000, 9000, 0))
	if !ok || math.Abs(pct-90) > 0.01 {
		t.Fatalf("additive pct = %.2f ok=%v", pct, ok)
	}
	pct, ok = TurnCacheHitPct("OPENAI", "gpt-5.6", realUsage(1000, 250, 0))
	if !ok || math.Abs(pct-25) > 0.01 {
		t.Fatalf("subset pct = %.2f ok=%v", pct, ok)
	}
}

func TestCacheStats_TTLFollowsConfiguredAnthropicTTL(t *testing.T) {
	t.Setenv(llmclient.PromptCacheTTLEnv, "1h")
	ct := NewCostTracker()
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", realUsage(500, 100, 100))
	if s := ct.CacheStats(); s.TTL != "1h" || !s.Warm {
		t.Fatalf("stats = %+v", s)
	}
	// Bedrock reports the marker it actually sent: 1h on Claude 4.5+, the
	// wire default on older Claude.
	ct2 := NewCostTracker()
	ct2.RecordRealUsage("BEDROCK", "global.anthropic.claude-sonnet-5", realUsage(500, 100, 100))
	if s := ct2.CacheStats(); s.TTL != "1h" {
		t.Fatalf("bedrock ttl = %s", s.TTL)
	}
	ct3 := NewCostTracker()
	ct3.RecordRealUsage("BEDROCK", "anthropic.claude-3-7-sonnet-20250219-v1:0", realUsage(500, 100, 100))
	if s := ct3.CacheStats(); s.TTL != "5m" {
		t.Fatalf("older Claude on bedrock ttl = %s", s.TTL)
	}
}

func TestFormatIdle(t *testing.T) {
	cases := map[time.Duration]string{20 * time.Second: "20s", 3 * time.Minute: "3m", 90 * time.Minute: "1h30m"}
	for d, want := range cases {
		if got := formatIdle(d); got != want {
			t.Fatalf("formatIdle(%s) = %s, want %s", d, got, want)
		}
	}
}

// A 1-hour cache write is billed at 2x input on Anthropic; the 5-minute
// share keeps 1.25x. The turn estimate and the session record must agree.
func TestCacheWrite1hPricing(t *testing.T) {
	input, _, known := lookupModelPricing("CLAUDEAI", "claude-sonnet-5")
	if !known || input <= 0 {
		t.Skip("pricing table entry missing")
	}
	u := &models.UsageInfo{PromptTokens: 0, CacheCreationInputTokens: 1_000_000, CacheCreation1hInputTokens: 1_000_000, IsReal: true}
	got := estimateTurnCostUSD("CLAUDEAI", "claude-sonnet-5", u)
	if math.Abs(got-input*2.0) > 1e-9 {
		t.Fatalf("1h write cost = %.6f, want %.6f (2x input)", got, input*2.0)
	}
	u5 := &models.UsageInfo{CacheCreationInputTokens: 1_000_000, IsReal: true}
	if got := estimateTurnCostUSD("CLAUDEAI", "claude-sonnet-5", u5); math.Abs(got-input*1.25) > 1e-9 {
		t.Fatalf("5m write cost = %.6f, want %.6f (1.25x input)", got, input*1.25)
	}
	ct := NewCostTracker()
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", u)
	if s := ct.TotalCost(); math.Abs(s-input*2.0) > 1e-9 {
		t.Fatalf("session cost = %.6f, want %.6f", s, input*2.0)
	}
}
