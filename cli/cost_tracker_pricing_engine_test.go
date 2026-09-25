/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"

	"github.com/diillson/chatcli/llm/pricing"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
)

// legacyRecomputeRecordCost is the record formula exactly as cost_tracker.go
// carried it before the pricing engine moved to llm/pricing — kept here,
// verbatim, as the oracle: the engine must reproduce it bit for bit, not
// approximately.
func legacyRecomputeRecordCost(rec *ModelUsageRecord) {
	inputCost, outputCost, known := lookupModelPricing(rec.Provider, rec.Model)
	rec.PricingKnown = known
	cacheWriteCost, cacheReadCost := getCachePricing(rec.Provider, rec.Model)

	unbilled := func(total, billed int64) int64 {
		if d := total - billed; d > 0 {
			return d
		}
		return 0
	}
	promptTokens := unbilled(rec.PromptTokens, rec.BilledPromptTokens)
	completionTokens := unbilled(rec.CompletionTokens, rec.BilledCompletionTokens)
	cacheRead := unbilled(rec.CacheReadTokens, rec.BilledCacheReadTokens)
	cacheCreation := unbilled(rec.CacheCreationTokens, rec.BilledCacheCreationTokens)

	if reasoningTokensAdditive(rec.Provider) {
		completionTokens += unbilled(rec.ReasoningTokens, 0)
	}

	billableInput := promptTokens
	if !cacheTokensAdditive(rec.Provider, rec.Model) && cacheRead > 0 && cacheReadCost > 0 {
		billableInput -= cacheRead
		if billableInput < 0 {
			billableInput = 0
		}
	}

	rec.InputCostUSD = float64(billableInput) / 1_000_000 * inputCost
	rec.OutputCostUSD = float64(completionTokens) / 1_000_000 * outputCost
	creation1h := rec.CacheCreation1hTokens
	if creation1h > cacheCreation {
		creation1h = cacheCreation
	}
	creation5m := cacheCreation - creation1h
	rec.CacheCostUSD = float64(creation5m)/1_000_000*cacheWriteCost +
		float64(creation1h)/1_000_000*cacheWrite1hCost(rec.Provider, rec.Model, cacheWriteCost) +
		float64(cacheRead)/1_000_000*cacheReadCost
	rec.TotalCostUSD = rec.InputCostUSD + rec.OutputCostUSD + rec.CacheCostUSD + rec.ProviderCostUSD
}

// pricingEngineGoldenRecords covers every branch of the formula: additive
// cache with a 1h share, subset carve-out, additive reasoning, a mixed
// provider-billed key, a subscription backend and an unpriced model.
func pricingEngineGoldenRecords() []*ModelUsageRecord {
	return []*ModelUsageRecord{
		{Provider: "CLAUDEAI", Model: "claude-sonnet-5", PromptTokens: 1_234_567, CompletionTokens: 98_765,
			CacheCreationTokens: 222_333, CacheCreation1hTokens: 44_444, CacheReadTokens: 555_555, ReasoningTokens: 777},
		{Provider: "CLAUDEAI", Model: "claude-fable-5-1", PromptTokens: 3_003, CompletionTokens: 1_001,
			CacheCreationTokens: 9_009, CacheCreation1hTokens: 90_000, CacheReadTokens: 70_007},
		{Provider: "OPENAI", Model: "gpt-5.4", PromptTokens: 1_000_001, CompletionTokens: 20_002,
			CacheReadTokens: 600_003, ReasoningTokens: 15_000},
		{Provider: "OPENAI", Model: "gpt-5.4", PromptTokens: 100, CacheReadTokens: 500},
		{Provider: "GOOGLEAI", Model: "gemini-3.1-pro", PromptTokens: 250_000, CompletionTokens: 12_345,
			ReasoningTokens: 6_789, CacheReadTokens: 100_000},
		{Provider: "OPENROUTER", Model: "openai/gpt-5.4", PromptTokens: 300_000, CompletionTokens: 30_000,
			BilledPromptTokens: 100_000, BilledCompletionTokens: 10_000, ProviderCostUSD: 0.5,
			CacheReadTokens: 50_000, BilledCacheReadTokens: 20_000},
		{Provider: "MINIMAX", Model: "minimax-m3", PromptTokens: 1_000_000, CacheReadTokens: 400_000},
		{Provider: "XAI", Model: "grok-4.7", PromptTokens: 77_777, CompletionTokens: 8_888, CacheReadTokens: 33_333},
		{Provider: "COPILOT", Model: "gpt-4o", PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
		{Provider: "UNKNOWN", Model: "mystery", PromptTokens: 1_000_000, CompletionTokens: 1_000_000},
	}
}

func TestRecomputeRecordCost_MatchesLegacyFormula(t *testing.T) {
	t.Setenv(pricing.OverrideEnv, "")
	for _, rec := range pricingEngineGoldenRecords() {
		want := *rec
		legacyRecomputeRecordCost(&want)
		got := *rec
		recomputeRecordCost(&got)
		// Exact float equality on purpose: the arithmetic moved verbatim,
		// so a single reordered operation would show up here.
		assert.Equal(t, want, got, "%s/%s", rec.Provider, rec.Model)
	}
}

func TestRecomputeRecordCost_GoldenValues(t *testing.T) {
	t.Setenv(pricing.OverrideEnv, "")
	// Sonnet 5: 1M prompt x $2, 100K output x $10, 150K 5m writes x $2.5,
	// 50K 1h writes x $4, 400K reads x $0.2.
	rec := &ModelUsageRecord{Provider: "CLAUDEAI", Model: "claude-sonnet-5",
		PromptTokens: 1_000_000, CompletionTokens: 100_000,
		CacheCreationTokens: 200_000, CacheCreation1hTokens: 50_000, CacheReadTokens: 400_000}
	recomputeRecordCost(rec)
	assert.True(t, rec.PricingKnown)
	assert.InDelta(t, 2.0, rec.InputCostUSD, 1e-12)
	assert.InDelta(t, 1.0, rec.OutputCostUSD, 1e-12)
	assert.InDelta(t, 0.655, rec.CacheCostUSD, 1e-12)
	assert.InDelta(t, 3.655, rec.TotalCostUSD, 1e-12)

	// gpt-5.4 subset schema: 600K of the 1M prompt are cache reads at
	// $1.25, the remaining 400K at $2.5; 10K output at $15.
	rec = &ModelUsageRecord{Provider: "OPENAI", Model: "gpt-5.4",
		PromptTokens: 1_000_000, CompletionTokens: 10_000, CacheReadTokens: 600_000}
	recomputeRecordCost(rec)
	assert.InDelta(t, 1.0, rec.InputCostUSD, 1e-12)
	assert.InDelta(t, 0.75, rec.CacheCostUSD, 1e-12)
	assert.InDelta(t, 0.15, rec.OutputCostUSD, 1e-12)
	assert.InDelta(t, 1.9, rec.TotalCostUSD, 1e-12)

	// An unpriced model is zero AND flagged unknown.
	rec = &ModelUsageRecord{Provider: "UNKNOWN", Model: "mystery", PromptTokens: 1_000_000}
	recomputeRecordCost(rec)
	assert.False(t, rec.PricingKnown)
	assert.Zero(t, rec.TotalCostUSD)
}

func TestEstimateTurnCostUSD_DelegatesToEngine(t *testing.T) {
	t.Setenv(pricing.OverrideEnv, "")
	usages := []*models.UsageInfo{
		nil,
		{PromptTokens: 1_000, CompletionTokens: 100, CostUSD: 0.42},
		{PromptTokens: 12_000, CompletionTokens: 3_000, CacheReadInputTokens: 8_000,
			CacheCreationInputTokens: 2_000, CacheCreation1hInputTokens: 500, InputTokensTotal: 22_000},
		{PromptTokens: 100_000, CompletionTokens: 10_000, CacheReadInputTokens: 200_000, InputTokensTotal: 300_000},
	}
	for _, u := range usages {
		want := pricing.CostOf("CLAUDEAI", "claude-sonnet-5", u).TotalUSD
		assert.Equal(t, want, estimateTurnCostUSD("CLAUDEAI", "claude-sonnet-5", u))
	}
	assert.InDelta(t, 0.42, estimateTurnCostUSD("CLAUDEAI", "claude-sonnet-5", usages[1]), 1e-12)
	// The long-context call is tiered, the record formula alone is not.
	rec := &ModelUsageRecord{Provider: "CLAUDEAI", Model: "claude-sonnet-5",
		PromptTokens: 100_000, CompletionTokens: 10_000, CacheReadTokens: 200_000}
	recomputeRecordCost(rec)
	assert.Greater(t, estimateTurnCostUSD("CLAUDEAI", "claude-sonnet-5", usages[3]), rec.TotalCostUSD)
}
