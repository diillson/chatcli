/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/models"
)

// TestLookupModelPricingKnownFlag pins the three pricing outcomes apart:
// table-priced, deliberately unmetered (known, zero) and genuinely unknown.
func TestLookupModelPricingKnownFlag(t *testing.T) {
	cases := []struct {
		provider, model string
		wantKnown       bool
		wantIn          float64
	}{
		{"CLAUDEAI", "claude-sonnet-5", true, 2.0}, // $2/$10 became the permanent Sonnet 5 price
		{"OPENAI", "gpt-4o", true, 2.50},
		{"DEEPSEEK", "deepseek-reasoner", true, 0.55}, // API alias of R1 — not the $0.27 generic tier
		{"OLLAMA", "llama3.3", true, 0},               // unmetered by design
		{"STACKSPOT", "stackspot-ai", true, 0},
		{"DEVIN", "claude-sonnet-5", true, 0}, // Devin short-circuit beats model heuristics
		{"UNKNOWN", "no-such-model", false, 0},
		{"OPENROUTER", "some/very-obscure-model", false, 0},
	}
	for _, c := range cases {
		in, _, known := lookupModelPricing(c.provider, c.model)
		if known != c.wantKnown || in != c.wantIn {
			t.Errorf("lookupModelPricing(%s,%s) = (in=%v, known=%v), want (in=%v, known=%v)",
				c.provider, c.model, in, known, c.wantIn, c.wantKnown)
		}
	}
}

// TestGetCachePricingFamilies pins the per-family cache discounts.
func TestGetCachePricingFamilies(t *testing.T) {
	cases := []struct {
		provider, model     string
		wantWrite, wantRead float64
	}{
		{"CLAUDEAI", "claude-sonnet-5", 2.0 * 1.25, 2.0 * 0.10},
		// Fable 5.1 reads at 2.5% of input ($0.25), writes keep 1.25x.
		{"CLAUDEAI", "claude-fable-5-1", 10.0 * 1.25, 10.0 * 0.025},
		{"BEDROCK", "anthropic.claude-fable-5-1", 10.0 * 1.25, 10.0 * 0.025},
		{"CLAUDEAI", "claude-fable-5", 10.0 * 1.25, 10.0 * 0.10}, // Fable 5 keeps the 10% rule
		{"OPENAI", "gpt-4o", 0, 2.50 * 0.50},
		{"GOOGLEAI", "gemini-2.5-pro", 0, 1.25 * 0.25},
		{"DEEPSEEK", "deepseek-chat", 0, 0.27 * 0.25},
		// xAI cached input (docs.x.ai pricing, Sep 2026): $0.50 on 4.6,
		// $0.30 on 4.5, $0.20 on the 4.3/4.20 tier.
		{"XAI", "grok-4.6", 0, 0.50},
		{"XAI", "grok-4.5", 0, 0.30},
		{"XAI", "grok-4.3", 0, 1.25 * 0.16},
		{"UNKNOWN", "no-such-model", 0, 0},
	}
	for _, c := range cases {
		w, r := getCachePricing(c.provider, c.model)
		if !almostEqual(w, c.wantWrite) || !almostEqual(r, c.wantRead) {
			t.Errorf("getCachePricing(%s,%s) = (%v,%v), want (%v,%v)",
				c.provider, c.model, w, r, c.wantWrite, c.wantRead)
		}
	}
}

func almostEqual(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

// TestRecomputeCostCacheSubsetSemantics: OpenAI reports cached tokens as a
// SUBSET of prompt_tokens — they must be billed once at the cache rate, not
// once full-price plus once discounted.
func TestRecomputeCostCacheSubsetSemantics(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	ct.RecordRealUsage("OPENAI", "gpt-4o", &models.UsageInfo{
		PromptTokens:         1_000_000, // includes the cached slice below
		CompletionTokens:     0,
		CacheReadInputTokens: 400_000,
		IsReal:               true,
	})
	rec := ct.modelUsage[modelKey("OPENAI", "gpt-4o")]
	// 600K at $2.50 + 400K at $1.25 = 1.50 + 0.50 = 2.00
	if !almostEqual(rec.TotalCostUSD, 2.00) {
		t.Fatalf("subset cache cost = %v, want 2.00 (input %v, cache %v)",
			rec.TotalCostUSD, rec.InputCostUSD, rec.CacheCostUSD)
	}

	// Anthropic reports cache tokens ALONGSIDE input_tokens — additive.
	ct2 := NewCostTracker()
	ct2.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", &models.UsageInfo{
		PromptTokens:         1_000_000,
		CacheReadInputTokens: 400_000,
		IsReal:               true,
	})
	rec2 := ct2.modelUsage[modelKey("CLAUDEAI", "claude-sonnet-5")]
	// 1M at $2.00 + 400K at $0.20 = 2.00 + 0.08
	if !almostEqual(rec2.TotalCostUSD, 2.08) {
		t.Fatalf("additive cache cost = %v, want 2.08", rec2.TotalCostUSD)
	}
}

// TestProviderCostOverride: a provider-reported billed amount (OpenRouter
// usage.cost) is authoritative over table math.
func TestProviderCostOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	ct.RecordRealUsage("OPENROUTER", "some/very-obscure-model", &models.UsageInfo{
		PromptTokens:     10_000,
		CompletionTokens: 2_000,
		CostUSD:          0.0421,
		IsReal:           true,
	})
	if !almostEqual(ct.TotalCost(), 0.0421) {
		t.Fatalf("provider cost override: total = %v, want 0.0421", ct.TotalCost())
	}
	rec := ct.modelUsage[modelKey("OPENROUTER", "some/very-obscure-model")]
	if rec.ProviderCostUSD == 0 {
		t.Fatal("ProviderCostUSD not accumulated")
	}
}

// TestMixedBilledAndTableCallsAddUp: a key mixing calls that reported
// usage.cost with calls that did not must price the uncovered tokens from
// the tables and ADD both parts — never let the billed amount clobber the
// table share (that under-reported spend and disarmed the budget gate).
func TestMixedBilledAndTableCallsAddUp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	// Call 1: provider-billed (usage.cost present).
	ct.RecordRealUsage("OPENROUTER", "openai/gpt-4o", &models.UsageInfo{
		PromptTokens: 2_000, CompletionTokens: 500, CostUSD: 0.01, IsReal: true,
	})
	// Call 2: no usage.cost — table pricing must cover it.
	// gpt-4o via openrouter re-dispatch: 1M in at $2.50 + 100K out at $10 = $3.50.
	ct.RecordRealUsage("OPENROUTER", "openai/gpt-4o", &models.UsageInfo{
		PromptTokens: 1_000_000, CompletionTokens: 100_000, IsReal: true,
	})
	if got, want := ct.TotalCost(), 0.01+3.50; !almostEqual(got, want) {
		t.Fatalf("mixed key total = %v, want %v (billed + table)", got, want)
	}
}

// TestZeroRateFamiliesNeverGetFreeCache: for families without a published
// cache discount (getCachePricing returns 0), cached tokens must stay
// billed at the plain input price — the subset carve-out with a zero read
// rate would make them free and under-report spend.
func TestZeroRateFamiliesNeverGetFreeCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	// GLM-5.2 ($1.40/M input, no cache rate): 100K prompt incl. 80K cached.
	ct.RecordRealUsage("ZAI", "glm-5.2", &models.UsageInfo{
		PromptTokens:         100_000,
		CacheReadInputTokens: 80_000,
		IsReal:               true,
	})
	// Full input price on ALL 100K: 0.1 × $1.40 = $0.14.
	if got := ct.TotalCost(); !almostEqual(got, 0.14) {
		t.Fatalf("zero-rate cache carve-out leaked: total = %v, want 0.14", got)
	}
}

// TestCacheSemanticsFollowReportingSchema: a Claude model served through an
// OpenAI-compatible gateway (OpenRouter) reports cached_tokens as a SUBSET
// of prompt_tokens — the additive Anthropic branch would bill them twice.
func TestCacheSemanticsFollowReportingSchema(t *testing.T) {
	if cacheTokensAdditive("OPENROUTER", "anthropic/claude-sonnet-5") {
		t.Fatal("openrouter-served claude treated as additive (double-bills cache)")
	}
	if !cacheTokensAdditive("CLAUDEAI", "claude-sonnet-5") {
		t.Fatal("native claude lost additive semantics")
	}
	if !cacheTokensAdditive("BEDROCK", "amazon.nova-pro-v1:0") {
		t.Fatal("Bedrock Converse reports inputTokens as uncached-only for every vendor: additive")
	}
	if cacheTokensAdditive("BEDROCK", "openai.gpt-5.6-terra") {
		t.Fatal("OpenAI family on Bedrock reports OpenAI-style subset usage: not additive")
	}
	if cacheTokensAdditive("BEDROCK", "openai.gpt-oss-120b-1:0") {
		t.Fatal("gpt-oss on Bedrock InvokeModel is OpenAI-schema: not additive")
	}
	if !cacheTokensAdditive("BEDROCK", "anthropic.claude-sonnet-5") {
		t.Fatal("bedrock claude lost additive semantics")
	}

	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	// 100K prompt (90K cached subset) via openrouter: 10K at $2/M + 90K at
	// the claude cache-read rate ($0.20/M) = 0.02 + 0.018.
	ct.RecordRealUsage("OPENROUTER", "anthropic/claude-sonnet-5", &models.UsageInfo{
		PromptTokens:         100_000,
		CacheReadInputTokens: 90_000,
		IsReal:               true,
	})
	if got := ct.TotalCost(); !almostEqual(got, 0.02+0.018) {
		t.Fatalf("openrouter claude cache math = %v, want 0.038", got)
	}
}

// TestOpenRouterUnknownFamilyModelStaysUnpriced: a slug matching a family
// substring but no pricing entry must propagate known=false so /cost lists
// it as unpriced instead of silently free.
func TestOpenRouterUnknownFamilyModelStaysUnpriced(t *testing.T) {
	_, _, known := lookupModelPricing("OPENROUTER", "anthropic/claude-nonexistent-99")
	if known {
		t.Fatal("family-substring match without a pricing entry reported known=true")
	}
}

// TestTurnAndSessionCacheMathAgree: estimateTurnCostUSD delegates to the
// record formula — cache-bearing turns must price identically both ways.
func TestTurnAndSessionCacheMathAgree(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	usage := &models.UsageInfo{
		PromptTokens:             200_000,
		CompletionTokens:         30_000,
		CacheReadInputTokens:     150_000,
		CacheCreationInputTokens: 10_000,
		IsReal:                   true,
	}
	for _, tc := range []struct{ provider, model string }{
		{"CLAUDEAI", "claude-sonnet-5"},
		{"OPENAI", "gpt-4o"},
		{"ZAI", "glm-5.2"},
		{"OPENROUTER", "anthropic/claude-sonnet-5"},
	} {
		ct := NewCostTracker()
		ct.RecordRealUsage(tc.provider, tc.model, usage)
		if got, want := estimateTurnCostUSD(tc.provider, tc.model, usage), ct.TotalCost(); !almostEqual(got, want) {
			t.Errorf("%s/%s: turn cost %v != session cost %v", tc.provider, tc.model, got, want)
		}
	}
}

// TestReasoningTokensAccumulate covers the informational reasoning total.
func TestReasoningTokensAccumulate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	ct.RecordRealUsage("OPENAI", "gpt-5.6", &models.UsageInfo{
		PromptTokens: 10, CompletionTokens: 500, ReasoningTokens: 300, IsReal: true,
	})
	if ct.totalReasoning != 300 {
		t.Fatalf("totalReasoning = %d, want 300", ct.totalReasoning)
	}
}

// TestResetStartsFreshPeriodAndPersistsOld: reset must never lose data —
// the closing period lands on disk and the live counters restart at zero.
func TestResetStartsFreshPeriodAndPersistsOld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	ct.RecordRealUsage("OPENAI", "gpt-4o", &models.UsageInfo{PromptTokens: 100, CompletionTokens: 50, IsReal: true})
	oldID := ct.CurrentSessionID()

	ct.Reset()

	if ct.TotalTokens() != 0 || ct.TotalCost() != 0 {
		t.Fatalf("reset left counters: tokens=%d cost=%v", ct.TotalTokens(), ct.TotalCost())
	}
	if ct.CurrentSessionID() == oldID {
		t.Fatal("reset kept the old session id")
	}
	snap, err := LoadCostSnapshot(oldID)
	if err != nil {
		t.Fatalf("closing period not persisted: %v", err)
	}
	if snap.TotalRequests != 1 {
		t.Fatalf("persisted snapshot requests = %d, want 1", snap.TotalRequests)
	}
}

// TestListCostSnapshotsOrdersMostRecentFirst also covers the round-trip.
func TestListCostSnapshotsOrdersMostRecentFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	ct.RecordRealUsage("OPENAI", "gpt-4o", &models.UsageInfo{PromptTokens: 100, CompletionTokens: 1, IsReal: true})
	if err := ct.SaveSession(); err != nil {
		t.Fatalf("save: %v", err)
	}
	snaps, err := ListCostSnapshots(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 1 || snaps[0].SessionID != ct.CurrentSessionID() {
		t.Fatalf("unexpected listing: %+v", snaps)
	}
	if snaps[0].TotalTokens != 101 {
		t.Fatalf("TotalTokens = %d, want 101", snaps[0].TotalTokens)
	}
}

// TestBudgetTransitionsAnnounceOncePerEscalation pins the one-shot notice.
func TestBudgetTransitionsAnnounceOncePerEscalation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CHATCLI_SESSION_BUDGET_USD", "1.00")
	t.Setenv("CHATCLI_BUDGET_WARNING_PCT", "0.5")
	ct := NewCostTracker()

	// Below warning: nothing to announce.
	ct.RecordRealUsage("OPENAI", "gpt-4o", &models.UsageInfo{PromptTokens: 40_000, IsReal: true}) // $0.10
	if _, _, ok := ct.TakeBudgetTransition(); ok {
		t.Fatal("announced below the warning threshold")
	}

	// Cross warning: announce exactly once.
	ct.RecordRealUsage("OPENAI", "gpt-4o", &models.UsageInfo{PromptTokens: 200_000, IsReal: true}) // +$0.50 → $0.60
	level, msg, ok := ct.TakeBudgetTransition()
	if !ok || level != BudgetWarning || msg == "" {
		t.Fatalf("warning transition: level=%v ok=%v msg=%q", level, ok, msg)
	}
	if _, _, ok := ct.TakeBudgetTransition(); ok {
		t.Fatal("warning announced twice")
	}

	// Cross exceeded: announce again.
	ct.RecordRealUsage("OPENAI", "gpt-4o", &models.UsageInfo{PromptTokens: 200_000, IsReal: true}) // +$0.50 → $1.10
	level, _, ok = ct.TakeBudgetTransition()
	if !ok || level != BudgetExceeded {
		t.Fatalf("exceeded transition: level=%v ok=%v", level, ok)
	}
}

// TestBudgetHardStopGate pins BudgetBlocked and its env reload.
func TestBudgetHardStopGate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CHATCLI_SESSION_BUDGET_USD", "0.10")
	t.Setenv("CHATCLI_BUDGET_HARD_STOP", "true")
	ct := NewCostTracker()

	if ct.BudgetBlocked() {
		t.Fatal("blocked before any spend")
	}
	ct.RecordRealUsage("OPENAI", "gpt-4o", &models.UsageInfo{PromptTokens: 100_000, IsReal: true}) // $0.25
	if !ct.BudgetBlocked() {
		t.Fatal("not blocked after exceeding the limit with hard stop armed")
	}

	// Raising the limit via env + ReloadBudget unblocks without restart.
	t.Setenv("CHATCLI_SESSION_BUDGET_USD", "5.00")
	ct.ReloadBudget()
	if ct.BudgetBlocked() {
		t.Fatal("still blocked after the limit was raised on reload")
	}
}

// TestEstimateTurnCostUSDMatchesTracker: footer math and session math must
// agree for the same single turn.
func TestEstimateTurnCostUSDMatchesTracker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	usage := &models.UsageInfo{
		PromptTokens:         100_000,
		CompletionTokens:     20_000,
		CacheReadInputTokens: 40_000,
		IsReal:               true,
	}
	ct := NewCostTracker()
	ct.RecordRealUsage("OPENAI", "gpt-4o", usage)
	if got, want := estimateTurnCostUSD("OPENAI", "gpt-4o", usage), ct.TotalCost(); !almostEqual(got, want) {
		t.Fatalf("turn cost %v != session cost %v for the same turn", got, want)
	}

	// Provider-billed cost wins outright.
	if got := estimateTurnCostUSD("OPENROUTER", "x/y", &models.UsageInfo{CostUSD: 0.5, PromptTokens: 1}); !almostEqual(got, 0.5) {
		t.Fatalf("provider cost not authoritative in turn estimate: %v", got)
	}
}

// TestUnpricedModelsSurfaceInsteadOfHiding: a model with tokens but no
// pricing entry must be listed, not silently absent from the total.
func TestUnpricedModelsSurfaceInsteadOfHiding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	ct.RecordRealUsage("SOMEPROVIDER", "mystery-model", &models.UsageInfo{PromptTokens: 1000, IsReal: true})
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	unpriced := unpricedModelsLocked(ct)
	if len(unpriced) != 1 || unpriced[0] != "SOMEPROVIDER/mystery-model" {
		t.Fatalf("unpriced listing = %v", unpriced)
	}
}
