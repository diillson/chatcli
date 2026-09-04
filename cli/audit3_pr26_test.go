/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"math"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestNovaPricing_RowsAndUnverifiedNova2(t *testing.T) {
	in, out, ok := lookupModelPricing("BEDROCK", "amazon.nova-pro-v1:0")
	if !ok || in != 0.80 || out != 3.20 {
		t.Fatalf("nova pro = %v %v %v", in, out, ok)
	}
	if in, out, ok := lookupModelPricing("BEDROCK", "us.amazon.nova-micro-v1:0"); !ok || in != 0.035 || out != 0.14 {
		t.Fatalf("nova micro = %v %v %v", in, out, ok)
	}
	if _, _, ok := lookupModelPricing("BEDROCK", "amazon.nova-2-lite-v1:0"); ok {
		t.Fatal("Nova 2 stays unpriced until its list price is verified (never a guess)")
	}
}

func TestCopilot_IsASubscriptionNotPerToken(t *testing.T) {
	in, out, ok := lookupModelPricing("COPILOT", "gpt-5.6")
	if !ok || in != 0 || out != 0 {
		t.Fatalf("copilot = %v %v %v", in, out, ok)
	}
}

func TestLongContextTiers_PerCall(t *testing.T) {
	if in, out := longContextMultipliers("CLAUDEAI", "claude-sonnet-5", 250_000); in != 2 || out != 1.5 {
		t.Fatalf("anthropic tier = %v %v", in, out)
	}
	if in, out := longContextMultipliers("CLAUDEAI", "claude-sonnet-5", 150_000); in != 1 || out != 1 {
		t.Fatal("under the threshold: no tier")
	}
	if in, out := longContextMultipliers("GOOGLEAI", "gemini-2.5-pro", 300_000); in != 2 || out != 1.5 {
		t.Fatalf("gemini tier = %v %v", in, out)
	}
	if in, out := longContextMultipliers("XAI", "grok-4", 200_000); in != 2 || out != 2 {
		t.Fatalf("grok tier = %v %v", in, out)
	}
	if in, out := longContextMultipliers("OPENAI", "gpt-5.6", 500_000); in != 1 || out != 1 {
		t.Fatal("openai has no tier")
	}
	small := &models.UsageInfo{PromptTokens: 100_000, CompletionTokens: 1000, IsReal: true}
	big := &models.UsageInfo{PromptTokens: 300_000, CompletionTokens: 1000, IsReal: true}
	base := estimateTurnCostUSD("CLAUDEAI", "claude-sonnet-5", small)
	tiered := estimateTurnCostUSD("CLAUDEAI", "claude-sonnet-5", big)
	// 3× the prompt at 2× the price, output at 1.5×: strictly more than 3× base.
	if base <= 0 || tiered <= base*3 {
		t.Fatalf("tier must raise the per-turn estimate: base=%v tiered=%v", base, tiered)
	}
	// The session tracker books the tiered call as a billed amount.
	ct := NewCostTracker()
	ct.RecordRealUsage("CLAUDEAI", "claude-sonnet-5", big)
	if math.Abs(ct.TotalCost()-tiered) > 1e-9 {
		t.Fatalf("tracker total %v != tiered %v", ct.TotalCost(), tiered)
	}
}
