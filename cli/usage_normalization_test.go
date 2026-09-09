/*
 * ChatCLI - Cross-provider usage normalization (CLI side)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The provider adapters classify their own payloads (models.UsageInfo.
 * Normalize); these tests pin what the CLI does with that classification:
 * one comparable input figure on screen, in /cost and in the window math,
 * whatever provider served the turn.
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

// additiveUsage is a Bedrock/Anthropic turn: a 19K prefix written to cache,
// 3 uncached tokens on top. subsetUsage is the same conversation on an
// OpenAI-schema provider.
func additiveUsage() *models.UsageInfo {
	u := &models.UsageInfo{PromptTokens: 3, CompletionTokens: 53,
		CacheCreationInputTokens: 19130, IsReal: true}
	u.Normalize(models.CacheAdditive)
	return u
}

func subsetUsage() *models.UsageInfo {
	u := &models.UsageInfo{PromptTokens: 19133, CompletionTokens: 53,
		CacheReadInputTokens: 19130, IsReal: true}
	u.Normalize(models.CacheSubset)
	return u
}

// TestContextTokensPrefersTheAdapterClassification: the normalized field wins
// over any guess made from the provider/model strings.
func TestContextTokensPrefersTheAdapterClassification(t *testing.T) {
	u := additiveUsage()
	// Deliberately a provider/model pair whose heuristic says "subset": the
	// adapter already said otherwise, and the adapter read the payload.
	if got := contextTokens("OPENAI", "gpt-6-astra", u); got != 19133 {
		t.Fatalf("contextTokens = %d, want 19133 (adapter's classification)", got)
	}
}

// TestContextTokensFallsBackForLegacyUsage: a record persisted before the
// field existed must keep behaving exactly as it did.
func TestContextTokensFallsBackForLegacyUsage(t *testing.T) {
	legacy := &models.UsageInfo{PromptTokens: 3, CompletionTokens: 53,
		CacheCreationInputTokens: 19130, IsReal: true} // no InputTokensTotal
	if got := contextTokens("BEDROCK", "anthropic.claude-sonnet-4-6", legacy); got != 19133 {
		t.Fatalf("additive fallback = %d, want 19133", got)
	}
	if got := contextTokens("OPENAI", "gpt-6-astra", legacy); got != 3 {
		t.Fatalf("subset fallback = %d, want 3", got)
	}
}

// TestBothSchemasRecordTheSameTokens is the headline regression: the same
// conversation must not read as 3 tokens on one provider and 19K on another.
func TestBothSchemasRecordTheSameTokens(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordRealUsage("BEDROCK", "global.anthropic.claude-sonnet-4-6", additiveUsage())
	ct.RecordRealUsage("OPENAI", "gpt-6-astra", subsetUsage())

	snap := ct.Snapshot()
	bedrock := snap.ModelUsage[modelKey("BEDROCK", "global.anthropic.claude-sonnet-4-6")]
	openai := snap.ModelUsage[modelKey("OPENAI", "gpt-6-astra")]

	if bedrock.InputTokens != openai.InputTokens {
		t.Fatalf("same input recorded differently: bedrock=%d openai=%d",
			bedrock.InputTokens, openai.InputTokens)
	}
	if bedrock.InputTokens != 19133 {
		t.Fatalf("InputTokens = %d, want 19133", bedrock.InputTokens)
	}
	if bedrock.TotalTokens != 19186 {
		t.Fatalf("TotalTokens = %d, want 19186 (the old math said 56)", bedrock.TotalTokens)
	}
	// The raw split must survive untouched — the cost math is built on it.
	if bedrock.PromptTokens != 3 || bedrock.CacheCreationTokens != 19130 {
		t.Fatalf("raw provider counts altered: %+v", bedrock)
	}
	if snap.TotalTokens != 19186*2 {
		t.Fatalf("session total = %d, want %d", snap.TotalTokens, 19186*2)
	}
}

// TestRecordedCostIsUnchangedByNormalization: normalization is a reporting
// change, never a billing one.
func TestRecordedCostIsUnchangedByNormalization(t *testing.T) {
	withField := NewCostTracker()
	withField.RecordRealUsage("CLAUDEAI", "claude-sonnet-4-6", additiveUsage())

	legacy := &models.UsageInfo{PromptTokens: 3, CompletionTokens: 53,
		CacheCreationInputTokens: 19130, IsReal: true}
	withoutField := NewCostTracker()
	withoutField.RecordRealUsage("CLAUDEAI", "claude-sonnet-4-6", legacy)

	if a, b := withField.TotalCost(), withoutField.TotalCost(); a != b {
		t.Fatalf("cost changed with normalization: %v vs %v", a, b)
	}
}

// TestLegacyRecordsStillCountInTheSessionTotal: sessions restored from disk
// have no InputTokens field; their PromptTokens must still be counted.
func TestLegacyRecordsStillCountInTheSessionTotal(t *testing.T) {
	rec := &ModelUsageRecord{PromptTokens: 500, CompletionTokens: 50}
	if got := recordInputTokens(rec); got != 500 {
		t.Fatalf("recordInputTokens = %d, want 500", got)
	}
	rec.InputTokens = 700
	if got := recordInputTokens(rec); got != 700 {
		t.Fatalf("recordInputTokens = %d, want 700", got)
	}
}

// TestLongContextTierIgnoresCachedTokensOnSubsetSchemas: cached tokens are
// already inside prompt_tokens there, and adding them could tip a request
// over a pricing threshold it never crossed.
func TestLongContextTierIgnoresCachedTokensOnSubsetSchemas(t *testing.T) {
	// 150K prompt of which 120K cached: under Gemini's 200K long-context
	// threshold. The old math summed to 270K and applied the 2x tier.
	u := &models.UsageInfo{PromptTokens: 150000, CompletionTokens: 100,
		CacheReadInputTokens: 120000, IsReal: true}
	u.Normalize(models.CacheSubset)

	plain := estimateTurnCostUSD("GOOGLEAI", "gemini-2.5-pro", u)

	over := &models.UsageInfo{PromptTokens: 250000, CompletionTokens: 100, IsReal: true}
	over.Normalize(models.CacheSubset)
	tiered := estimateTurnCostUSD("GOOGLEAI", "gemini-2.5-pro", over)

	if plain <= 0 || tiered <= 0 {
		t.Skip("no pricing for this model in the catalog")
	}
	// Per-token, the genuinely-long request must cost more than the one that
	// only looked long because its cache was double-counted.
	perTokenPlain := plain / 150000
	perTokenTiered := tiered / 250000
	if perTokenPlain >= perTokenTiered {
		t.Fatalf("cached tokens still tipping the long-context tier: %v vs %v",
			perTokenPlain, perTokenTiered)
	}
}

// TestFormatTokenSummaryShowsTheWholeInput pins the envelope header.
func TestFormatTokenSummaryShowsTheWholeInput(t *testing.T) {
	got := formatTokenSummary(additiveUsage())
	if got == formatTokenSummary(&models.UsageInfo{PromptTokens: 3, CompletionTokens: 53}) {
		t.Fatalf("header still rendering the uncached delta: %q", got)
	}
}

// TestTelemetryPartsSizesAgainstTheServedModel: a skill hint or route
// override can serve a turn on a different model than the session's. The
// cost was already attributed to the served pair; the window and cache
// semantics beside it must follow, or one turn is described by two models.
func TestTelemetryPartsSizesAgainstTheServedModel(t *testing.T) {
	session := &ChatCLI{Provider: "OPENAI", Model: "gpt-4o"} // 128K window
	usage := &models.UsageInfo{PromptTokens: 64000, CompletionTokens: 100, IsReal: true}
	usage.Normalize(models.CacheSubset)

	own := strings.Join(session.telemetryParts("", "", usage, 0, false), " · ")
	served := strings.Join(session.telemetryParts("BEDROCK", "global.anthropic.claude-sonnet-5", usage, 0, false), " · ")

	if own == served {
		t.Fatalf("served pair ignored: both rendered %q", own)
	}
	if !strings.Contains(own, "ctx 50%") {
		t.Fatalf("session pair: %q, want ctx 50%% of the 128K window", own)
	}
	if !strings.Contains(served, "ctx 6%") {
		t.Fatalf("served pair: %q, want ctx 6%% of the 1M window", served)
	}
}

// TestModelFamilyKey: a ratio measured for a model must be findable from any
// provider's spelling of that same model.
func TestModelFamilyKey(t *testing.T) {
	cases := map[string]string{
		"global.anthropic.claude-sonnet-4-6-20260115-v1:0": "claude-sonnet-4-6",
		"us.anthropic.claude-opus-5":                       "claude-opus-5",
		"anthropic.claude-sonnet-4-6":                      "claude-sonnet-4-6",
		"claude-sonnet-4-6":                                "claude-sonnet-4-6",
		"gpt-6-astra":                                      "gpt-6-astra",
		"gpt-5.6-luna":                                     "gpt-5.6-luna",
		"MiniMax-M2.7":                                     "minimax-m2.7",
	}
	for in, want := range cases {
		if got := modelFamilyKey(in); got != want {
			t.Errorf("modelFamilyKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCalibratorBorrowsTheSameModelsRatio: without this, a provider that has
// never been sampled runs on the blind 4.0 default while a sibling entry has
// already measured the very same tokenizer — which is how one conversation
// showed a different ctx% on each provider.
func TestCalibratorBorrowsTheSameModelsRatio(t *testing.T) {
	c := newTokenCalibrator()
	// 47 samples learned on the direct Anthropic surface.
	for i := 0; i < 47; i++ {
		c.Observe("CLAUDEAI", "claude-sonnet-4-6", 3170, 1000)
	}

	ratio, samples := c.CharsPerToken("BEDROCK", "global.anthropic.claude-sonnet-4-6-20260115-v1:0")
	if ratio == defaultCharsPerToken {
		t.Fatalf("Bedrock still on the blind default: %v", ratio)
	}
	if samples == 0 {
		t.Fatalf("borrowed ratio reported as unsampled")
	}
	if ratio < 3.0 || ratio > 3.4 {
		t.Fatalf("borrowed ratio = %v, want ~3.17", ratio)
	}

	// An unrelated model still gets the default — the fallback matches the
	// model, not merely "something was learned once".
	if r, _ := c.CharsPerToken("DEVIN", "kimi-k3"); r != defaultCharsPerToken {
		t.Fatalf("unrelated model borrowed a ratio: %v", r)
	}
}

// TestCalibratorExactPairWins: a measured pair is never overridden by a
// sibling's ratio.
func TestCalibratorExactPairWins(t *testing.T) {
	c := newTokenCalibrator()
	for i := 0; i < 5; i++ {
		c.Observe("CLAUDEAI", "claude-sonnet-4-6", 3170, 1000) // 3.17
		c.Observe("BEDROCK", "anthropic.claude-sonnet-4-6", 2000, 1000)
	}
	ratio, _ := c.CharsPerToken("BEDROCK", "anthropic.claude-sonnet-4-6")
	if ratio > 2.5 {
		t.Fatalf("own samples lost to the sibling: %v", ratio)
	}
}

// TestCacheAccountingFollowsThePayload: MiniMax serves the same model over
// an OpenAI-shaped and an Anthropic-shaped endpoint. The model name cannot
// tell them apart; the payload can, and the cache hit ratio depends on it.
func TestCacheAccountingFollowsThePayload(t *testing.T) {
	anthropicSurface := &models.UsageInfo{PromptTokens: 400, CompletionTokens: 20,
		CacheCreationInputTokens: 1000, CacheReadInputTokens: 9000, IsReal: true}
	anthropicSurface.Normalize(models.CacheAdditive)

	openaiSurface := &models.UsageInfo{PromptTokens: 10400, CompletionTokens: 20,
		CacheReadInputTokens: 9000, IsReal: true}
	openaiSurface.Normalize(models.CacheSubset)

	if !usageCacheAccounting("MINIMAX", "MiniMax-M2.7", anthropicSurface) {
		t.Fatal("Anthropic-shaped payload read as subset")
	}
	if usageCacheAccounting("MINIMAX", "MiniMax-M2.7", openaiSurface) {
		t.Fatal("OpenAI-shaped payload read as additive")
	}

	// Both describe the same 10.4K input with 9K served from cache: the hit
	// ratio must match, whichever endpoint answered.
	a, okA := TurnCacheHitPct("MINIMAX", "MiniMax-M2.7", anthropicSurface)
	b, okB := TurnCacheHitPct("MINIMAX", "MiniMax-M2.7", openaiSurface)
	if !okA || !okB {
		t.Fatal("cache telemetry unreported")
	}
	if clampPct(a) != clampPct(b) {
		t.Fatalf("same cache state read differently: %.1f%% vs %.1f%%", a, b)
	}
}

// TestCacheAccountingFallsBackWithoutCacheActivity: with no cache tokens the
// payload cannot say which schema it is, and nothing was cached anyway — the
// provider/model heuristic stays in charge.
func TestCacheAccountingFallsBackWithoutCacheActivity(t *testing.T) {
	u := &models.UsageInfo{PromptTokens: 500, CompletionTokens: 10, IsReal: true}
	u.Normalize(models.CacheSubset)
	if !usageCacheAccounting("CLAUDEAI", "claude-sonnet-4-6", u) {
		t.Fatal("heuristic not consulted when the payload is silent")
	}
}
