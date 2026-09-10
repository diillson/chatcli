/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/pricing"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDevinStaticPricing pins the static Cognition table against the ids
// the CLI accepts in both spellings, with the fast/priority surcharge and
// the longest-family-wins rule.
func TestDevinStaticPricing(t *testing.T) {
	cases := []struct {
		model           string
		wantIn, wantOut float64
		wantOK          bool
	}{
		{"claude-sonnet-4.6", 3, 15, true},
		{"claude-sonnet-4-6", 3, 15, true},
		{"CLAUDE-OPUS-4.8", 5, 25, true},
		{"claude-opus-4-8-medium-fast", 10, 50, true},
		{"claude-opus-5-xhigh", 5, 25, true},
		{"claude-opus-5-max-fast", 10, 50, true},
		{"claude-sonnet-5-fast", 2, 10, true}, // no fast variant listed: base rate
		{"gpt-5.6-sol", 1.2, 6, true},
		{"gpt-5-6-sol-high-priority", 8, 40, true},
		{"gpt-5.6-luna-none-priority", 0.4, 2.4, true},
		{"gpt-5.4", 2.5, 15, true},
		{"gpt-5.4-mini", 0.75, 4.5, true}, // longest family wins over gpt-5.4
		{"gpt-5-4-mini", 0.75, 4.5, true},
		{"gpt-5.5-none-priority", 12.5, 75, true},
		{"gpt-5.3-codex-xhigh", 1.75, 14, true},
		{"glm-5.3", 1.4, 4.4, true},
		{"glm-5.3-flash", 0.15, 0.5, true},
		{"glm-5-3", 1.4, 4.4, true},
		{"kimi-k3", 3, 15, true},
		{"deepseek-v4-flash", 0.14, 0.28, true},
		{"swe-1.7-lightning", 2.5, 12.5, true},
		{"swe-1.7", 0.5, 2.5, true},
		{"swe-1.6-fast", 0.5, 2.5, true}, // a family of its own, not a fast variant
		{"gemini-3-flash", 0.5, 3, true},
		{"gemini-3.5-flash", 1.5, 9, true},
		// Never listed with a price: stays unknown rather than guessed.
		{"claude-fable-5.1", 0, 0, false},
		{"grok-4.6", 0, 0, false},
		{"adaptive", 0, 0, false},
		{"gpt-5.5x", 0, 0, false}, // a longer id must continue with a separator
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			in, out, ok := devinStaticPricing(tc.model)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantIn, in)
			assert.Equal(t, tc.wantOut, out)
		})
	}
}

// TestLookupModelPricing_Precedence: operator override, then the rate the
// account listed, then the static table, then known-zero.
func TestLookupModelPricing_Precedence(t *testing.T) {
	t.Cleanup(func() { pricing.ResetProvider(catalog.ProviderDevin) })
	t.Setenv(pricing.OverrideEnv, "")

	// Static table only.
	in, out, known := lookupModelPricing("DEVIN", "claude-sonnet-4.6")
	assert.True(t, known)
	assert.Equal(t, 3.0, in)
	assert.Equal(t, 15.0, out)

	// The account listing outranks the static table.
	pricing.Register(catalog.ProviderDevin, "claude-sonnet-4.6", pricing.Rate{InputPerMTok: 2.5, OutputPerMTok: 12})
	in, out, _ = lookupModelPricing("DEVIN", "claude-sonnet-4.6")
	assert.Equal(t, 2.5, in)
	assert.Equal(t, 12.0, out)

	// The operator outranks the listing, and a wildcard prices what nothing
	// else does (fable was never listed with a price).
	t.Setenv(pricing.OverrideEnv, "DEVIN:claude-sonnet-4.6=4/20;DEVIN:*=1/5")
	in, out, _ = lookupModelPricing("DEVIN", "claude-sonnet-4.6")
	assert.Equal(t, 4.0, in)
	assert.Equal(t, 20.0, out)
	in, out, known = lookupModelPricing("DEVIN", "claude-fable-5.1")
	assert.True(t, known)
	assert.Equal(t, 1.0, in)
	assert.Equal(t, 5.0, out)

	// A variant resolves to its catalog family for the override too.
	t.Setenv(pricing.OverrideEnv, "DEVIN:claude-opus-5=7/35")
	in, _, _ = lookupModelPricing("DEVIN", "claude-opus-5-xhigh")
	assert.Equal(t, 7.0, in)

	// Any provider, not just Devin: the override beats the static tables
	// and a known-zero subscription alike.
	t.Setenv(pricing.OverrideEnv, "CLAUDEAI:claude-sonnet-4-6=1/2;COPILOT:*=0.5/1")
	in, out, _ = lookupModelPricing("CLAUDEAI", "claude-sonnet-4-6")
	assert.Equal(t, 1.0, in)
	assert.Equal(t, 2.0, out)
	in, out, known = lookupModelPricing("COPILOT", "gpt-4o")
	assert.True(t, known)
	assert.Equal(t, 0.5, in)
	assert.Equal(t, 1.0, out)

	// Without any source a never-priced Devin family is known-zero, which
	// /cost surfaces as "no rate known" instead of a silent free.
	t.Setenv(pricing.OverrideEnv, "")
	in, out, known = lookupModelPricing("DEVIN", "claude-fable-5.1")
	assert.True(t, known)
	assert.Zero(t, in)
	assert.Zero(t, out)
}

func TestPromptTokensForRecord_ReconcilesPayloadSchemaWithNameConvention(t *testing.T) {
	// Subset payload (enterprise Devin ATIF standard block) for a Claude
	// model, whose name convention is additive: only the uncached delta
	// goes into PromptTokens.
	subsetClaude := &models.UsageInfo{PromptTokens: 14274, CacheReadInputTokens: 9098, CacheCreationInputTokens: 5173, InputTokensTotal: 14274}
	assert.Equal(t, 3, promptTokensForRecord("DEVIN", "claude-sonnet-4.6", subsetClaude))

	// Additive payload (Devin-native metrics, Anthropic direct) for a
	// Claude model: consistent, untouched.
	additiveClaude := &models.UsageInfo{PromptTokens: 3, CacheReadInputTokens: 9098, CacheCreationInputTokens: 5173, InputTokensTotal: 14274}
	assert.Equal(t, 3, promptTokensForRecord("DEVIN", "claude-sonnet-4.6", additiveClaude))
	assert.Equal(t, 3, promptTokensForRecord("CLAUDEAI", "claude-sonnet-4-6", additiveClaude))

	// Subset payload for a subset-convention model: consistent, untouched.
	subsetKimi := &models.UsageInfo{PromptTokens: 12383, CacheReadInputTokens: 12032, InputTokensTotal: 12383}
	assert.Equal(t, 12383, promptTokensForRecord("DEVIN", "kimi-k3", subsetKimi))

	// Additive payload under a subset convention: reads fold back into the
	// prompt (the subset formula carves them out again), writes stay out.
	additiveOther := &models.UsageInfo{PromptTokens: 100, CacheReadInputTokens: 50, CacheCreationInputTokens: 20, InputTokensTotal: 170}
	assert.Equal(t, 150, promptTokensForRecord("DEVIN", "kimi-k3", additiveOther))

	// No schema fact (estimate / older session): count used as-is.
	legacy := &models.UsageInfo{PromptTokens: 14274, CacheReadInputTokens: 9098}
	assert.Equal(t, 14274, promptTokensForRecord("DEVIN", "claude-sonnet-4.6", legacy))
	assert.Zero(t, promptTokensForRecord("DEVIN", "claude-sonnet-4.6", nil))
}

// TestRecordRealUsage_EnterpriseDevinClaudeTurnIsBilledOnce prices the
// real turn end to end: 3 uncached input tokens at $3, 9098 reads at
// $0.30, 5173 writes at $3.75, 4 output at $15 — never the 14274 whole
// input at $3 on top of the cache rates.
func TestRecordRealUsage_EnterpriseDevinClaudeTurnIsBilledOnce(t *testing.T) {
	t.Cleanup(func() { pricing.ResetProvider(catalog.ProviderDevin) })
	t.Setenv(pricing.OverrideEnv, "")
	ct := NewCostTrackerAt(t.TempDir())

	usage := &models.UsageInfo{
		PromptTokens: 14274, CompletionTokens: 4,
		CacheReadInputTokens: 9098, CacheCreationInputTokens: 5173,
		InputTokensTotal: 14274, TotalTokens: 14278, IsReal: true,
	}
	ct.RecordRealUsage("DEVIN", "claude-sonnet-4.6", usage)

	rec := ct.modelUsage[modelKey("DEVIN", "claude-sonnet-4.6")]
	require.NotNil(t, rec)
	assert.True(t, rec.PricingKnown)
	assert.Equal(t, int64(3), rec.PromptTokens)
	assert.Equal(t, int64(14274), rec.InputTokens, "the input the model held stays the whole input")
	want := 3*3.0/1e6 + 9098*0.30/1e6 + 5173*3.75/1e6 + 4*15.0/1e6
	assert.InDelta(t, want, rec.TotalCostUSD, 1e-9)
	assert.InDelta(t, want, ct.TotalCost(), 1e-9)
	// The per-turn estimate is the same formula.
	assert.InDelta(t, want, estimateTurnCostUSD("DEVIN", "claude-sonnet-4.6", usage), 1e-9)
}

func TestUnmeteredModelsLocked(t *testing.T) {
	t.Setenv(pricing.OverrideEnv, "")
	ct := NewCostTrackerAt(t.TempDir())
	ct.RecordRealUsage("DEVIN", "claude-fable-5.1", &models.UsageInfo{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11, InputTokensTotal: 10, IsReal: true})
	ct.RecordRealUsage("DEVIN", "claude-sonnet-4.6", &models.UsageInfo{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11, InputTokensTotal: 10, IsReal: true})
	ct.RecordRealUsage("OLLAMA", "llama3", &models.UsageInfo{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11, InputTokensTotal: 10, IsReal: true})
	ct.RecordRealUsage("COPILOT", "gpt-4o", &models.UsageInfo{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11, InputTokensTotal: 10, IsReal: true})

	ct.mu.RLock()
	got := unmeteredModelsLocked(ct)
	ct.mu.RUnlock()
	assert.Equal(t, []string{"COPILOT/gpt-4o", "DEVIN/claude-fable-5.1"}, got,
		"priced and local-free models are not listed; unknown-rate ones are")

	t.Setenv(pricing.OverrideEnv, "DEVIN:*=1/5")
	ct.mu.Lock()
	for _, rec := range ct.modelUsage {
		recomputeRecordCost(rec)
	}
	got = unmeteredModelsLocked(ct)
	ct.mu.Unlock()
	assert.Equal(t, []string{"COPILOT/gpt-4o"}, got, "an override takes the model off the list")
}

func TestModelPricingOverrideStatus(t *testing.T) {
	t.Setenv(pricing.OverrideEnv, "")
	assert.NotContains(t, modelPricingOverrideStatus(), "override", "unset renders like any other env row")

	t.Setenv(pricing.OverrideEnv, "DEVIN:*=1/5;broken")
	got := modelPricingOverrideStatus()
	assert.Contains(t, got, "DEVIN:*=1/5;broken")
	assert.Contains(t, got, "1 override")
	assert.Contains(t, got, "broken", "the malformed entry is named")
}
