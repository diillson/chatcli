/*
 * ChatCLI - Tests for the Nov 2026 pricing refresh
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pricing

import (
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

// List and cache rates of the models added in the Nov 2026 refresh, per
// surface, next to the sibling each id is a superstring of.
func TestNov2026_ListAndCacheRates(t *testing.T) {
	withClock(t, time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		provider, model      string
		in, out, write, read float64
	}{
		// platform.claude.com pricing: Fable 5.2 $8/$40, write $10, read $0.20.
		{"CLAUDEAI", "claude-fable-5-2", 8, 40, 10, 0.20},
		{"BEDROCK", "global.anthropic.claude-fable-5-2", 8, 40, 10, 0.20},
		{"OPENROUTER", "anthropic/claude-fable-5.2", 8, 40, 10, 0.20},
		// Fable 5.1 and 5 keep $10/$50.
		{"CLAUDEAI", "claude-fable-5-1", 10, 50, 12.5, 0.25},
		{"CLAUDEAI", "claude-fable-5", 10, 50, 12.5, 1},
		// developers.openai.com pricing: GPT-6.1 Terra $1/$5, write $1.25, read $0.05.
		{"OPENAI", "gpt-6.1-terra", 1, 5, 1.25, 0.05},
		{"BEDROCK", "global.openai.gpt-6.1-terra", 1, 5, 1.25, 0.05},
		{"OPENROUTER", "openai/gpt-6.1-terra", 1, 5, 1.25, 0.05},
		{"OPENAI", "gpt-6.1-sol", 2, 10, 2.5, 0.10},
		// aistudio.google.com pricing: Carbon at Argon's introductory $2/$10, cached $0.10.
		{"GOOGLEAI", "gemini-4-carbon", 2, 10, 0, 0.10},
		{"OPENROUTER", "google/gemini-4-carbon", 2, 10, 0, 0.10},
		// platform.kimi.ai pricing: K3.1 Pro $2/$10, highspeed $4/$20, cached $0.20 on both.
		{"MOONSHOT", "kimi-k3.1-pro", 2, 10, 0, 0.20},
		{"MOONSHOT", "kimi-k3.1-pro-highspeed", 4, 20, 0, 0.20},
		{"MOONSHOT", "kimi-k3", 3, 15, 0, 0.30},
		{"OPENROUTER", "moonshotai/kimi-k3.1-pro", 2, 10, 0, 0.20},
		// Bedrock card: only the highspeed variant, $4/$20, write $5, read $0.20.
		{"BEDROCK", "global.moonshotai.kimi-k3.1", 4, 20, 5, 0.20},
		{"BEDROCK", "global.moonshotai.kimi-k3", 3, 15, 0, 0.30},
		// platform.minimax.io pricing: M3.1 Flash $0.15/$0.60, cached $0.015.
		{"MINIMAX", "MiniMax-M3.1-Flash", 0.15, 0.60, 0, 0.015},
		{"MINIMAX", "MiniMax-M3", 0.30, 1.20, 0, 0.06},
		// docs.z.ai pricing: FlashX-Legal at FlashX's $0.37/$1.25, cached $0.075.
		{"ZAI", "glm-5.3-flashx-legal", 0.37, 1.25, 0, 0.075},
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

// Carbon follows Argon's dated price: $4/$20 and $0.20 cached from Dec 1.
func TestNov2026_GeminiCarbonStandardPrice(t *testing.T) {
	withClock(t, time.Date(2026, time.December, 1, 0, 0, 0, 0, time.FixedZone("PST", -8*60*60)))
	in, out, known := ListPrice("GOOGLEAI", "gemini-4-carbon")
	_, read := CacheRates("GOOGLEAI", "gemini-4-carbon")
	if !known || !near(in, 4) || !near(out, 20) || !near(read, 0.20) {
		t.Fatalf("carbon standard price %v/%v read=%v known=%v", in, out, read, known)
	}
}

func TestNov2026_LongContextTiers(t *testing.T) {
	for _, tc := range []struct {
		provider, model string
		prompt          int
		in, out         float64
	}{
		// GPT-6.1 Terra: 2x input / 1.5x output above 272K.
		{"OPENAI", "gpt-6.1-terra", 272_000, 1, 1},
		{"OPENAI", "gpt-6.1-terra", 272_001, 2, 1.5},
		{"BEDROCK", "global.openai.gpt-6.1-terra", 500_000, 2, 1.5},
		// Fable 5.2 runs the full 1M at the standard price.
		{"CLAUDEAI", "claude-fable-5-2", 900_000, 1, 1},
		// M3.1 Flash is flat; M3 keeps its 512K step.
		{"MINIMAX", "MiniMax-M3.1-Flash", 900_000, 1, 1},
		{"MINIMAX", "MiniMax-M3", 900_000, 2, 2},
		{"GOOGLEAI", "gemini-4-carbon", 900_000, 1, 1},
		{"MOONSHOT", "kimi-k3.1-pro", 900_000, 1, 1},
	} {
		in, out := LongContextMultipliers(tc.provider, tc.model, tc.prompt)
		if in != tc.in || out != tc.out {
			t.Errorf("%s/%s at %d: %v/%v, want %v/%v", tc.provider, tc.model, tc.prompt, in, out, tc.in, tc.out)
		}
	}
}

// One Fable 5.2 call end to end, Anthropic usage shape (cache tokens come
// on top of input_tokens).
func TestNov2026_Fable52CallCost(t *testing.T) {
	u := &models.UsageInfo{
		PromptTokens:             100_000,
		CacheReadInputTokens:     800_000,
		CacheCreationInputTokens: 50_000,
		CompletionTokens:         20_000,
	}
	c := CostOf("CLAUDEAI", "claude-fable-5-2", u)
	// 100K fresh at $8, 800K read at $0.20, 50K written at $10, 20K out at $40.
	want := 0.1*8 + 0.8*0.20 + 0.05*10 + 0.02*40
	if !c.Known || !near(c.TotalUSD, want) {
		t.Fatalf("call cost %+v, want total %v", c, want)
	}
}
