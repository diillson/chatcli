/*
 * ChatCLI - evidence-based prompt-cache lifetime
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The hour-long cache entry costs 2x the write instead of 1.25x. Choosing it
 * up front is a bet: pure loss on a rapid-fire session, a large win on one
 * that pauses. These tests pin that the choice is made from what the session
 * actually did, once, and never against an explicit user setting.
 */
package cli

import (
	"testing"
	"time"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// resetCacheTTLState clears the process-wide lifetime decision so each test
// starts from the default.
func resetCacheTTLState(t *testing.T) {
	t.Helper()
	llmclient.SetPromptCacheTTLHint("5m")
	llmclient.ResetPromptCacheTTL()
	t.Cleanup(func() {
		llmclient.SetPromptCacheTTLHint("5m")
		llmclient.ResetPromptCacheTTL()
	})
}

func cachedTurn(write, read int) *models.UsageInfo {
	u := &models.UsageInfo{
		PromptTokens: 40, CompletionTokens: 200,
		CacheCreationInputTokens: write, CacheReadInputTokens: read, IsReal: true,
	}
	u.Normalize(models.CacheAdditive)
	return u
}

// TestTTLPromotesAfterAnIdleExpiry: two turns, a ten-minute pause, the prefix
// rebuilt — exactly what the hour would have prevented.
func TestTTLPromotesAfterAnIdleExpiry(t *testing.T) {
	resetCacheTTLState(t)
	t.Setenv(llmclient.PromptCacheTTLEnv, "")

	ct := NewCostTracker()
	const provider, model = "CLAUDEAI", "claude-sonnet-4-6"
	t0 := time.Now()

	ct.cache.observe(provider, model, cachedTurn(20000, 0), t0)
	ct.promoteCacheTTLIfIdling(provider, model)
	if llmclient.AnthropicCacheTTL() != "5m" {
		t.Fatal("promoted on the very first write, before any evidence")
	}
	llmclient.ResetPromptCacheTTL()

	// Ten minutes later the prefix had to be written again.
	ct.cache.observe(provider, model, cachedTurn(20000, 0), t0.Add(10*time.Minute))
	ct.promoteCacheTTLIfIdling(provider, model)
	if got := llmclient.AnthropicCacheTTL(); got != "1h" {
		t.Fatalf("lifetime = %q after an idle expiry, want 1h", got)
	}
}

// TestTTLStaysShortForARapidFireSession: turns seconds apart never expire, so
// the hour would be pure extra cost.
func TestTTLStaysShortForARapidFireSession(t *testing.T) {
	resetCacheTTLState(t)
	t.Setenv(llmclient.PromptCacheTTLEnv, "")

	ct := NewCostTracker()
	const provider, model = "CLAUDEAI", "claude-sonnet-4-6"
	t0 := time.Now()
	for i := 0; i < 5; i++ {
		ct.cache.observe(provider, model, cachedTurn(20000, 0), t0.Add(time.Duration(i)*20*time.Second))
		ct.promoteCacheTTLIfIdling(provider, model)
	}
	if got := llmclient.AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("lifetime = %q without any idle expiry, want 5m", got)
	}
}

// TestTTLNeverOverridesAnExplicitSetting: a user who pinned the env decided.
func TestTTLNeverOverridesAnExplicitSetting(t *testing.T) {
	resetCacheTTLState(t)
	t.Setenv(llmclient.PromptCacheTTLEnv, "5m")

	ct := NewCostTracker()
	const provider, model = "CLAUDEAI", "claude-sonnet-4-6"
	t0 := time.Now()
	ct.cache.observe(provider, model, cachedTurn(20000, 0), t0)
	ct.cache.observe(provider, model, cachedTurn(20000, 0), t0.Add(10*time.Minute))
	ct.promoteCacheTTLIfIdling(provider, model)

	if ct.cacheTTLPromoted {
		t.Fatal("promoted over an explicit CHATCLI_PROMPT_CACHE_TTL")
	}
	if got := llmclient.AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("lifetime = %q, want the pinned 5m", got)
	}
}

// TestTTLPromotionSkipsModelsWithoutTheHour: asking for a lifetime the model
// cannot carry would send an invalid marker.
func TestTTLPromotionSkipsModelsWithoutTheHour(t *testing.T) {
	resetCacheTTLState(t)
	t.Setenv(llmclient.PromptCacheTTLEnv, "")

	ct := NewCostTracker()
	const provider, model = "OPENAI", "gpt-6-astra"
	t0 := time.Now()
	ct.cache.observe(provider, model, cachedTurn(20000, 0), t0)
	ct.cache.observe(provider, model, cachedTurn(20000, 0), t0.Add(10*time.Minute))
	ct.promoteCacheTTLIfIdling(provider, model)

	if ct.cacheTTLPromoted {
		t.Fatal("promoted on a provider with no extended TTL")
	}
}

// TestTTLPromotionHappensOnce: a promotion is one-way and must not re-fire.
func TestTTLPromotionHappensOnce(t *testing.T) {
	resetCacheTTLState(t)
	t.Setenv(llmclient.PromptCacheTTLEnv, "")

	ct := NewCostTracker()
	const provider, model = "CLAUDEAI", "claude-sonnet-4-6"
	t0 := time.Now()
	ct.cache.observe(provider, model, cachedTurn(20000, 0), t0)
	ct.cache.observe(provider, model, cachedTurn(20000, 0), t0.Add(10*time.Minute))
	ct.promoteCacheTTLIfIdling(provider, model)
	if !ct.cacheTTLPromoted {
		t.Fatal("no promotion after an idle expiry")
	}
	// A user pinning 5m afterwards must not be re-overridden by a second call.
	llmclient.SetPromptCacheTTLHint("5m")
	llmclient.ResetPromptCacheTTL()
	ct.promoteCacheTTLIfIdling(provider, model)
	if got := llmclient.AnthropicCacheTTL(); got != "5m" {
		t.Fatalf("promotion re-fired: lifetime = %q", got)
	}
}

// TestSupportsExtendedCacheTTL pins which families can carry the hour.
func TestSupportsExtendedCacheTTL(t *testing.T) {
	cases := map[bool][][2]string{
		true:  {{"CLAUDEAI", "claude-sonnet-4-6"}, {"CLAUDEAI", "claude-fable-5-1"}},
		false: {{"OPENAI", "gpt-6-astra"}, {"GOOGLEAI", "gemini-3-pro"}, {"DEVIN", "kimi-k3"}},
	}
	for want, pairs := range cases {
		for _, p := range pairs {
			if got := supportsExtendedCacheTTL(p[0], p[1]); got != want {
				t.Errorf("supportsExtendedCacheTTL(%s, %s) = %v, want %v", p[0], p[1], got, want)
			}
		}
	}
}
