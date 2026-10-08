/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pricing

import (
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordCost_AnthropicAdditiveWith1hShare(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	// Sonnet 5: $2/$10, cache write 2.5 (5m) / 4 (1h), read 0.2.
	c := RecordCost(RecordInput{
		Provider: "CLAUDEAI", Model: "claude-sonnet-5",
		PromptTokens: 1_000_000, CompletionTokens: 100_000,
		CacheCreationTokens: 200_000, CacheCreation1hTokens: 50_000,
		CacheReadTokens: 400_000,
	})
	require.True(t, c.Known)
	assert.InDelta(t, 2.0, c.InputUSD, 1e-12)
	assert.InDelta(t, 1.0, c.OutputUSD, 1e-12)
	// 150K x 2.5 + 50K x 4 + 400K x 0.2 = 0.375 + 0.2 + 0.08
	assert.InDelta(t, 0.655, c.CacheUSD, 1e-12)
	assert.InDelta(t, 3.655, c.TotalUSD, 1e-12)
}

func TestRecordCost_1hShareNeverExceedsCreation(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	c := RecordCost(RecordInput{
		Provider: "CLAUDEAI", Model: "claude-sonnet-5",
		CacheCreationTokens: 100_000, CacheCreation1hTokens: 500_000,
	})
	// Every write token is a 1h write: 100K x 4.
	assert.InDelta(t, 0.4, c.CacheUSD, 1e-12)
}

func TestRecordCost_SubsetCarveOut(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	// gpt-5.4: $2.5/$15, cache read at 10% = 0.25, no write rate. The
	// 600K cached tokens are a SUBSET of the 1M prompt: billed once, at
	// the read rate.
	c := RecordCost(RecordInput{
		Provider: "OPENAI", Model: "gpt-5.4",
		PromptTokens: 1_000_000, CompletionTokens: 10_000, CacheReadTokens: 600_000,
	})
	assert.InDelta(t, 0.4*2.5, c.InputUSD, 1e-12)
	assert.InDelta(t, 0.6*0.25, c.CacheUSD, 1e-12)
	assert.InDelta(t, 0.15, c.OutputUSD, 1e-12)

	// Cached tokens beyond the prompt count clamp the billable input at 0.
	c = RecordCost(RecordInput{Provider: "OPENAI", Model: "gpt-5.4", PromptTokens: 100, CacheReadTokens: 500})
	assert.Zero(t, c.InputUSD)
}

func TestRecordCost_NoCacheRateKeepsInputPrice(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	// MiniMax-Text-01 has no cache read rate in the tables: the carve-out
	// must NOT run, otherwise the cached tokens would be free.
	c := RecordCost(RecordInput{
		Provider: "MINIMAX", Model: "minimax-text-01",
		PromptTokens: 1_000_000, CacheReadTokens: 400_000,
	})
	assert.InDelta(t, 0.20, c.InputUSD, 1e-12)
	assert.Zero(t, c.CacheUSD)
}

func TestRecordCost_UnpricedCacheWriteBillsAsInput(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	// MiniMax-M3 on its Anthropic-shaped surface reports writes beside the
	// prompt and publishes no write rate: 1M written tokens bill at the
	// $0.30 input price instead of nothing. Reads keep their own rate.
	c := RecordCost(RecordInput{
		Provider: "MINIMAX", Model: "minimax-m3",
		PromptTokens: 0, CacheCreationTokens: 1_000_000, CacheReadTokens: 1_000_000,
	})
	assert.InDelta(t, 0.30+0.06, c.CacheUSD, 1e-12)
	// A family with a published write rate is untouched (Sonnet 5: $2.50).
	c = RecordCost(RecordInput{Provider: "CLAUDEAI", Model: "claude-sonnet-5", CacheCreationTokens: 1_000_000})
	assert.InDelta(t, 2.5, c.CacheUSD, 1e-12)
}

func TestRecordCost_GeminiReasoningIsAdditive(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	c := RecordCost(RecordInput{
		Provider: "GOOGLEAI", Model: "gemini-3.1-pro",
		CompletionTokens: 100_000, ReasoningTokens: 100_000,
	})
	// 200K x $12.
	assert.InDelta(t, 2.4, c.OutputUSD, 1e-12)

	o := RecordCost(RecordInput{
		Provider: "OPENAI", Model: "gpt-5.4",
		CompletionTokens: 100_000, ReasoningTokens: 100_000,
	})
	// OpenAI reasoning is already inside completion_tokens: 100K x $15.
	assert.InDelta(t, 1.5, o.OutputUSD, 1e-12)
}

func TestRecordCost_BilledPoolsAndProviderCost(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	// Two calls on one key: the first billed by the provider (0.5 USD for
	// 100K/10K), the second not. Only the second's tokens hit the tables.
	c := RecordCost(RecordInput{
		Provider: "OPENROUTER", Model: "openai/gpt-5.4",
		PromptTokens: 300_000, CompletionTokens: 30_000,
		BilledPromptTokens: 100_000, BilledCompletionTokens: 10_000,
		ProviderCostUSD: 0.5,
	})
	assert.InDelta(t, 0.2*2.5, c.InputUSD, 1e-12)
	assert.InDelta(t, 0.02*15, c.OutputUSD, 1e-12)
	assert.InDelta(t, 0.5+0.5+0.3, c.TotalUSD, 1e-12)

	// Billed pools larger than the totals (a restored ledger) clamp to 0.
	z := RecordCost(RecordInput{
		Provider: "OPENAI", Model: "gpt-5.4",
		PromptTokens: 10, BilledPromptTokens: 20, ProviderCostUSD: 0.01,
	})
	assert.Zero(t, z.InputUSD)
	assert.InDelta(t, 0.01, z.TotalUSD, 1e-12)
}

func TestRecordCost_UnknownModelIsZeroAndUnknown(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	c := RecordCost(RecordInput{Provider: "UNKNOWN", Model: "mystery", PromptTokens: 1_000_000})
	assert.False(t, c.Known)
	assert.Zero(t, c.TotalUSD)

	k := RecordCost(RecordInput{Provider: "OLLAMA", Model: "llama3", PromptTokens: 1_000_000})
	assert.True(t, k.Known, "self-hosted is known-zero, not unknown")
	assert.Zero(t, k.TotalUSD)
}

func TestCostOf_ProviderBilledWinsOutright(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	c := CostOf("OPENROUTER", "openai/gpt-5.4", &models.UsageInfo{
		PromptTokens: 1_000_000, CompletionTokens: 1_000_000, CostUSD: 0.42,
	})
	assert.Equal(t, Cost{TotalUSD: 0.42, Known: true}, c)
	assert.Equal(t, Cost{}, CostOf("OPENAI", "gpt-5.4", nil))
}

func TestCostOf_MatchesRecordCostForOneCall(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	u := &models.UsageInfo{
		PromptTokens: 12_000, CompletionTokens: 3_000,
		CacheReadInputTokens: 8_000, CacheCreationInputTokens: 2_000,
		CacheCreation1hInputTokens: 500, ReasoningTokens: 100,
		InputTokensTotal: 22_000,
	}
	got := CostOf("CLAUDEAI", "claude-opus-5-5", u)
	want := RecordCost(RecordInput{
		Provider: "CLAUDEAI", Model: "claude-opus-5-5",
		PromptTokens: 12_000, CompletionTokens: 3_000, ReasoningTokens: 100,
		CacheReadTokens: 8_000, CacheCreationTokens: 2_000, CacheCreation1hTokens: 500,
	})
	assert.Equal(t, want, got)
	assert.True(t, got.Known)
	assert.Greater(t, got.TotalUSD, 0.0)
}

func TestCostOf_LongContextTier(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	// 300K of Sonnet 4.5 context (1M beta): 2x input and cache, 1.5x output.
	u := &models.UsageInfo{
		PromptTokens: 100_000, CompletionTokens: 10_000,
		CacheReadInputTokens: 200_000, InputTokensTotal: 300_000,
	}
	const legacy = "claude-sonnet-4-5"
	base := RecordCost(callRecordInput("CLAUDEAI", legacy, u))
	got := CostOf("CLAUDEAI", legacy, u)
	require.True(t, got.Known)
	assert.InDelta(t, base.InputUSD*2, got.InputUSD, 1e-12)
	assert.InDelta(t, base.CacheUSD*2, got.CacheUSD, 1e-12)
	assert.InDelta(t, base.OutputUSD*1.5, got.OutputUSD, 1e-12)
	assert.InDelta(t, (base.InputUSD+base.CacheUSD)*2+base.OutputUSD*1.5, got.TotalUSD, 1e-12)
	assert.InDelta(t, got.TotalUSD, TieredCallCostUSD("CLAUDEAI", legacy, u), 1e-12)

	// Claude 4.6+ runs the full 1M at standard price: the same 300K call
	// on Sonnet 5 / Opus 5.5 is not tiered (it used to bill 2x/1.5x).
	for _, m := range []string{"claude-sonnet-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1", "claude-sonnet-4-6"} {
		assert.Zero(t, TieredCallCostUSD("CLAUDEAI", m, u), "%s has no long-context premium", m)
		assert.InDelta(t, RecordCost(callRecordInput("CLAUDEAI", m, u)).TotalUSD, CostOf("CLAUDEAI", m, u).TotalUSD, 1e-12)
	}

	// Haiku 5.5 past 100K: every line 5x — $0.50 input, $0.05 cache read,
	// $2.50 output, exactly the official over-100K row.
	h := &models.UsageInfo{PromptTokens: 100_000, CompletionTokens: 10_000, CacheReadInputTokens: 100_000, InputTokensTotal: 200_000}
	hc := CostOf("CLAUDEAI", "claude-haiku-5-5", h)
	require.True(t, hc.Known)
	assert.InDelta(t, 0.1*0.50, hc.InputUSD, 1e-12)
	assert.InDelta(t, 0.1*0.05, hc.CacheUSD, 1e-12)
	assert.InDelta(t, 0.01*2.50, hc.OutputUSD, 1e-12)

	// Below the threshold nothing is tiered.
	small := &models.UsageInfo{PromptTokens: 1_000, CompletionTokens: 10, InputTokensTotal: 1_000}
	assert.Zero(t, TieredCallCostUSD("CLAUDEAI", legacy, small))
	// An unpriced model in a tier stays zero rather than guessing.
	// (gpt-6-terra sits in the GPT-6 tier but has no price: no 6.x Terra.)
	assert.Zero(t, TieredCallCostUSD("OPENAI", "gpt-6-terra", u))
	// A provider-billed call is never re-tiered.
	billed := *u
	billed.CostUSD = 1
	assert.Zero(t, TieredCallCostUSD("CLAUDEAI", legacy, &billed))
	assert.Zero(t, TieredCallCostUSD("CLAUDEAI", legacy, nil))
}

// TestLongContextMultipliers pins each vendor's published long-context
// tier at its exact threshold (Oct 2026 pricing pages): strict ">" for
// Anthropic, Google, OpenAI and MiniMax, ">=" for xAI ("reaches").
func TestLongContextMultipliers(t *testing.T) {
	type mc struct {
		provider, model string
		tokens          int
		in, out         float64
	}
	for _, tc := range []mc{
		// Anthropic: only the pre-4.6 Sonnets with the 1M beta.
		{"CLAUDEAI", "claude-sonnet-4-5", 200_000, 1, 1},
		{"CLAUDEAI", "claude-sonnet-4-5", 200_001, 2, 1.5},
		{"BEDROCK", "global.anthropic.claude-sonnet-4-5-20250929-v1:0", 300_000, 2, 1.5},
		{"CLAUDEAI", "claude-sonnet-4-6", 900_000, 1, 1},
		{"CLAUDEAI", "claude-sonnet-5", 900_000, 1, 1},
		{"CLAUDEAI", "claude-opus-5-5", 900_000, 1, 1},
		{"BEDROCK", "anthropic.claude-opus-5", 900_000, 1, 1},
		{"OPENROUTER", "anthropic/claude-fable-5.1", 900_000, 1, 1},
		// Haiku 5.5: 5x past 100K, on every surface spelling.
		{"CLAUDEAI", "claude-haiku-5-5", 100_000, 1, 1},
		{"CLAUDEAI", "claude-haiku-5-5", 100_001, 5, 5},
		{"BEDROCK", "global.anthropic.claude-haiku-5-5", 150_000, 5, 5},
		{"OPENROUTER", "anthropic/claude-haiku-5.5", 150_000, 5, 5},
		{"CLAUDEAI", "claude-haiku-4-5-20251001", 150_000, 1, 1},
		// Google: 2.5 Pro and 3.1 Pro split at 200K; Flash lines do not.
		{"GOOGLEAI", "gemini-2.5-pro", 250_000, 2, 1.5},
		{"GOOGLEAI", "gemini-3.1-pro-preview", 200_000, 1, 1},
		{"GOOGLEAI", "gemini-3.1-pro-preview", 200_001, 2, 1.5},
		{"GOOGLEAI", "gemini-3.8-flash", 900_000, 1, 1},
		// xAI: 2x on both from 200K (it used to fire at 128K).
		{"XAI", "grok-4.7", 199_999, 1, 1},
		{"XAI", "grok-4.7", 200_000, 2, 2},
		{"XAI", "grok-4.3", 150_000, 1, 1},
		{"XAI", "grok-build-0.1", 200_000, 2, 2},
		{"OPENROUTER", "x-ai/grok-4.7", 200_000, 2, 2},
		{"BEDROCK", "global.xai.grok-4.7", 250_000, 2, 2},
		// OpenAI: GPT-6 / 5.6 / 5.5 / 5.4 above 272K.
		{"OPENAI", "gpt-6.1-sol", 272_000, 1, 1},
		{"OPENAI", "gpt-6.1-sol", 272_001, 2, 1.5},
		{"OPENAI", "gpt-6-astra", 300_000, 2, 1.5},
		{"OPENAI", "gpt-5.6-luna", 300_000, 2, 1.5},
		{"OPENAI", "gpt-5.4", 900_000, 2, 1.5},
		{"BEDROCK", "global.openai.gpt-6.1-sol", 300_000, 2, 1.5},
		{"OPENAI", "gpt-5.4-mini", 300_000, 1, 1},
		{"OPENAI", "gpt-5.5-pro", 300_000, 1, 1},
		{"OPENAI", "gpt-4.1", 900_000, 1, 1},
		// MiniMax-M3: 2x on both above 512K.
		{"MINIMAX", "minimax-m3", 512_000, 1, 1},
		{"MINIMAX", "minimax-m3", 512_001, 2, 2},
		{"MINIMAX", "minimax-m2.7", 900_000, 1, 1},
	} {
		in, out := LongContextMultipliers(tc.provider, tc.model, tc.tokens)
		assert.Equal(t, tc.in, in, "%s/%s@%d in", tc.provider, tc.model, tc.tokens)
		assert.Equal(t, tc.out, out, "%s/%s@%d out", tc.provider, tc.model, tc.tokens)
	}
}

func TestSchemaRules(t *testing.T) {
	assert.True(t, CacheTokensAdditive("CLAUDEAI", "claude-sonnet-5"))
	assert.True(t, CacheTokensAdditive("BEDROCK", "anthropic.claude-sonnet-5"))
	assert.True(t, CacheTokensAdditive("BEDROCK", "amazon.nova-pro-v1:0"))
	assert.False(t, CacheTokensAdditive("BEDROCK", "openai.gpt-oss-120b"))
	assert.False(t, CacheTokensAdditive("BEDROCK", "global.openai.gpt-5.6-sol"))
	assert.False(t, CacheTokensAdditive("OPENROUTER", "anthropic/claude-sonnet-5"))
	assert.False(t, CacheTokensAdditive("OPENAI", "gpt-5.4"))
	assert.True(t, CacheTokensAdditive("DEVIN", "claude-opus-5"))

	assert.True(t, ReasoningTokensAdditive("googleai"))
	assert.True(t, ReasoningTokensAdditive(" GEMINI "))
	assert.False(t, ReasoningTokensAdditive("OPENAI"))
}

func TestPromptTokensForRecord(t *testing.T) {
	assert.Zero(t, PromptTokensForRecord("CLAUDEAI", "claude-sonnet-5", nil))

	// No schema fact: the count is used as-is.
	plain := &models.UsageInfo{PromptTokens: 100, CacheReadInputTokens: 40}
	assert.Equal(t, 100, PromptTokensForRecord("CLAUDEAI", "claude-sonnet-5", plain))

	// Subset payload under an additive name (the enterprise Devin ATIF
	// case): keep only the part outside the cache.
	devin := &models.UsageInfo{PromptTokens: 14274, CacheReadInputTokens: 9098, CacheCreationInputTokens: 5173, InputTokensTotal: 14274}
	assert.Equal(t, 3, PromptTokensForRecord("DEVIN", "claude-sonnet-4.6", devin))
	under := &models.UsageInfo{PromptTokens: 100, CacheReadInputTokens: 150, InputTokensTotal: 100}
	assert.Equal(t, 0, PromptTokensForRecord("CLAUDEAI", "claude-sonnet-5", under))

	// Additive payload under a subset name: whole input = prompt + reads.
	gw := &models.UsageInfo{PromptTokens: 100, CacheReadInputTokens: 50, CacheCreationInputTokens: 20, InputTokensTotal: 170}
	assert.Equal(t, 150, PromptTokensForRecord("OPENROUTER", "anthropic/claude-sonnet-5", gw))

	// Name and payload agree: unchanged.
	agree := &models.UsageInfo{PromptTokens: 100, CacheReadInputTokens: 50, InputTokensTotal: 150}
	assert.Equal(t, 100, PromptTokensForRecord("CLAUDEAI", "claude-sonnet-5", agree))
}

func TestContextTokens(t *testing.T) {
	assert.Zero(t, ContextTokens("CLAUDEAI", "claude-sonnet-5", nil))
	// The adapter's own classification wins.
	assert.Equal(t, 500, ContextTokens("OPENAI", "gpt-5.4", &models.UsageInfo{PromptTokens: 100, InputTokensTotal: 500}))
	// Fallback by name: additive adds the cache pools, subset does not.
	u := &models.UsageInfo{PromptTokens: 100, CacheReadInputTokens: 300, CacheCreationInputTokens: 50}
	assert.Equal(t, 450, ContextTokens("CLAUDEAI", "claude-sonnet-5", u))
	assert.Equal(t, 100, ContextTokens("OPENAI", "gpt-5.4", u))
}
