/*
 * ChatCLI - Tests for the mid-Nov 2026 pricing refresh
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pricing

import (
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

// List and cache rates of GPT-6.2 Astra / Sol, Gemini 4 Neon / Hydrogen
// and the Mythos line, per surface, next to the siblings whose ids they
// extend.
func TestNov2026b_ListAndCacheRates(t *testing.T) {
	withClock(t, time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		provider, model      string
		in, out, write, read float64
	}{
		// developers.openai.com pricing: Astra $8/$40, write $10, read $0.40.
		{"OPENAI", "gpt-6.2-astra", 8, 40, 10, 0.40},
		{"BEDROCK", "global.openai.gpt-6.2-astra", 8, 40, 10, 0.40},
		{"OPENROUTER", "openai/gpt-6.2-astra", 8, 40, 10, 0.40},
		// Sol $2/$10, write $2.50, read $0.10.
		{"OPENAI", "gpt-6.2-sol", 2, 10, 2.5, 0.10},
		{"BEDROCK", "us.openai.gpt-6.2-sol", 2, 10, 2.5, 0.10},
		{"OPENROUTER", "openai/gpt-6.2-sol", 2, 10, 2.5, 0.10},
		// GPT-6 Astra keeps its own, higher, price and 10% cache read.
		{"OPENAI", "gpt-6-astra", 10, 50, 12.5, 1},
		// aistudio.google.com pricing: Neon $3/$15, cached $0.15.
		{"GOOGLEAI", "gemini-4-neon", 3, 15, 0, 0.15},
		{"OPENROUTER", "google/gemini-4-neon", 3, 15, 0, 0.15},
		// Hydrogen $1.50/$7.50, cached $0.075.
		{"GOOGLEAI", "gemini-4-hydrogen", 1.5, 7.5, 0, 0.075},
		{"OPENROUTER", "google/gemini-4-hydrogen", 1.5, 7.5, 0, 0.075},
		// Carbon is still on the introductory $2/$10.
		{"GOOGLEAI", "gemini-4-carbon", 2, 10, 0, 0.10},
		// platform.claude.com pricing: Mythos at its Fable sibling's rates.
		{"CLAUDEAI", "claude-mythos-5-2", 8, 40, 10, 0.20},
		{"BEDROCK", "global.anthropic.claude-mythos-5-2", 8, 40, 10, 0.20},
		{"CLAUDEAI", "claude-mythos-5-1", 10, 50, 12.5, 0.25},
		{"CLAUDEAI", "claude-mythos-5", 10, 50, 12.5, 1},
	} {
		in, out, known := ListPrice(tc.provider, tc.model)
		if !known || !near(in, tc.in) || !near(out, tc.out) {
			t.Errorf("%s/%s list %v/%v known=%v, want %v/%v", tc.provider, tc.model, in, out, known, tc.in, tc.out)
		}
		write, read := CacheRates(tc.provider, tc.model)
		if !near(write, tc.write) || !near(read, tc.read) {
			t.Errorf("%s/%s cache write=%v read=%v, want %v/%v", tc.provider, tc.model, write, read, tc.write, tc.read)
		}
	}
}

// GPT-6.2 bills 2x input and 1.5x output for a request above 272K input
// tokens; Neon and Hydrogen have no long-context tier.
func TestNov2026b_LongContext(t *testing.T) {
	for _, tc := range []struct {
		provider, model string
		prompt          int
		in, out         float64
	}{
		{"OPENAI", "gpt-6.2-astra", 272_000, 1, 1},
		{"OPENAI", "gpt-6.2-astra", 272_001, 2, 1.5},
		{"OPENAI", "gpt-6.2-sol", 900_000, 2, 1.5},
		{"BEDROCK", "global.openai.gpt-6.2-sol", 300_000, 2, 1.5},
		{"GOOGLEAI", "gemini-4-neon", 900_000, 1, 1},
		{"GOOGLEAI", "gemini-4-hydrogen", 900_000, 1, 1},
	} {
		in, out := LongContextMultipliers(tc.provider, tc.model, tc.prompt)
		if !near(in, tc.in) || !near(out, tc.out) {
			t.Errorf("%s/%s at %d: %vx/%vx, want %vx/%vx", tc.provider, tc.model, tc.prompt, in, out, tc.in, tc.out)
		}
	}
	// One long call on Astra: 300K in at $16, 10K out at $60.
	u := &models.UsageInfo{PromptTokens: 300_000, CompletionTokens: 10_000}
	if got, want := TieredCallCostUSD("OPENAI", "gpt-6.2-astra", u), 300_000*16.0/1e6+10_000*60.0/1e6; !near(got, want) {
		t.Errorf("tiered Astra call = %v, want %v", got, want)
	}
}

// Cognition's dated step for GPT-5.6 Sol and the families its table added.
func TestNov2026b_DevinRates(t *testing.T) {
	step := time.Date(2026, time.November, 21, 7, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		at      time.Time
		model   string
		in, out float64
	}{
		{step.Add(-time.Second), "gpt-5.6-sol", 4, 20},
		{step.Add(-time.Second), "gpt-5-6-sol-high-priority", 8, 40},
		{step, "gpt-5.6-sol", 5, 30},
		{step, "gpt-5-6-sol-high-priority", 10, 60},
		// The step is Sol's alone.
		{step, "gpt-5.6-terra", 2, 12},
		{step, "gemini-4-neon-xhigh", 3, 15},
		{step, "deepseek-v4-1-flash-1017-high", 0.22, 0.66},
		{step, "deepseek-v4-flash-high", 0.14, 0.28},
	} {
		withClock(t, tc.at)
		in, out, ok := DevinStaticRate(tc.model)
		if !ok || !near(in, tc.in) || !near(out, tc.out) {
			t.Errorf("%s at %s = %v/%v ok=%v, want %v/%v", tc.model, tc.at.Format(time.RFC3339), in, out, ok, tc.in, tc.out)
		}
	}
	// Not in Cognition's table yet: unpriced rather than guessed.
	for _, model := range []string{"gpt-6.2-astra", "gpt-6-2-sol-high", "gemini-4-hydrogen"} {
		if _, _, ok := DevinStaticRate(model); ok {
			t.Errorf("%s must stay unpriced on Devin", model)
		}
	}
}
