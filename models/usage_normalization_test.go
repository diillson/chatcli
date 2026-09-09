/*
 * ChatCLI - Usage schema normalization tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package models

import "testing"

// TestNormalizeAdditive: the Anthropic/Bedrock schema reports cache reads and
// writes BESIDE the prompt count, so the normalized input is the sum. This is
// the case that rendered a 22K-token turn as "3 tokens in".
func TestNormalizeAdditive(t *testing.T) {
	u := &UsageInfo{PromptTokens: 3, CompletionTokens: 53,
		CacheCreationInputTokens: 19130, CacheReadInputTokens: 0, TotalTokens: 56}
	u.Normalize(CacheAdditive)

	if got := u.InputTotal(); got != 19133 {
		t.Fatalf("InputTotal = %d, want 19133", got)
	}
	if u.TotalTokens != 19186 {
		t.Fatalf("TotalTokens = %d, want 19186", u.TotalTokens)
	}
	if u.PromptTokens != 3 {
		t.Fatalf("PromptTokens must stay as the provider reported it (cost math): %d", u.PromptTokens)
	}
}

// TestNormalizeSubset: OpenAI-family cached tokens are already inside the
// prompt count — adding them would double-count.
func TestNormalizeSubset(t *testing.T) {
	u := &UsageInfo{PromptTokens: 22858, CompletionTokens: 28,
		CacheReadInputTokens: 10515, TotalTokens: 22886}
	u.Normalize(CacheSubset)

	if got := u.InputTotal(); got != 22858 {
		t.Fatalf("InputTotal = %d, want 22858", got)
	}
	if u.TotalTokens != 22886 {
		t.Fatalf("TotalTokens = %d, want 22886", u.TotalTokens)
	}
}

// TestNormalizeSubsetNeverSmallerThanItsCache guards the defensive branch: a
// subset schema cannot hold more cached tokens than the prompt count they are
// a subset of, so the bigger number wins instead of an impossible input.
func TestNormalizeSubsetNeverSmallerThanItsCache(t *testing.T) {
	u := &UsageInfo{PromptTokens: 100, CompletionTokens: 10, CacheReadInputTokens: 900}
	u.Normalize(CacheSubset)
	if got := u.InputTotal(); got != 900 {
		t.Fatalf("InputTotal = %d, want 900", got)
	}
}

// TestNormalizeKeepsProviderTotal: Gemini folds thoughtsTokenCount into its own
// totalTokenCount. Normalization may raise a total, never shrink one.
func TestNormalizeKeepsProviderTotal(t *testing.T) {
	u := &UsageInfo{PromptTokens: 1000, CompletionTokens: 200, ReasoningTokens: 500, TotalTokens: 1700}
	u.Normalize(CacheSubset)
	if u.TotalTokens != 1700 {
		t.Fatalf("TotalTokens = %d, want the provider's 1700", u.TotalTokens)
	}
}

// TestInputTotalFallsBackToPromptTokens: usage restored from a session written
// before the field existed must read exactly as it did then.
func TestInputTotalFallsBackToPromptTokens(t *testing.T) {
	u := &UsageInfo{PromptTokens: 500, CompletionTokens: 10}
	if got := u.InputTotal(); got != 500 {
		t.Fatalf("InputTotal = %d, want 500", got)
	}
	var nilUsage *UsageInfo
	if got := nilUsage.InputTotal(); got != 0 {
		t.Fatalf("nil InputTotal = %d, want 0", got)
	}
}

// TestMergeAccumulatesNormalizedInput keeps a merged pair honest even when one
// side predates normalization.
func TestMergeAccumulatesNormalizedInput(t *testing.T) {
	a := &UsageInfo{PromptTokens: 10, CompletionTokens: 1, CacheReadInputTokens: 90}
	a.Normalize(CacheAdditive)
	b := &UsageInfo{PromptTokens: 7, CompletionTokens: 2} // legacy: not normalized
	a.Merge(b)

	if got := a.InputTotal(); got != 107 {
		t.Fatalf("merged InputTotal = %d, want 107", got)
	}
}

// TestEstimateFromCharsCarriesInputTotal: an estimate has no cache split, so
// the normalized input is the estimate itself — never zero, which would make
// the envelope drop the telemetry.
func TestEstimateFromCharsCarriesInputTotal(t *testing.T) {
	u := EstimateFromChars(4000, 400)
	if u.InputTotal() != 1000 {
		t.Fatalf("InputTotal = %d, want 1000", u.InputTotal())
	}
}

// TestMergeLegacyLeftOperandKeepsItsInput: a += on the normalized field
// would drop the left side's own input when only the right side carries one.
func TestMergeLegacyLeftOperandKeepsItsInput(t *testing.T) {
	a := &UsageInfo{PromptTokens: 50, CompletionTokens: 1} // legacy
	b := &UsageInfo{PromptTokens: 100, CompletionTokens: 2}
	b.Normalize(CacheSubset)
	a.Merge(b)
	if got := a.InputTotal(); got != 150 {
		t.Fatalf("merged InputTotal = %d, want 150", got)
	}
}
