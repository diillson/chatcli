/*
 * ChatCLI - Tests for Gemini 4 Argon pricing
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pricing

import (
	"math"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

func withClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = prev })
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// ai.google.dev/gemini-api/docs/pricing (Oct 2026): $2/$10 with cached
// input at $0.10 through Nov 30 2026; $4/$20 and $0.20 from Dec 1 2026.
func TestGeminiArgon_DatedPrice(t *testing.T) {
	pt := time.FixedZone("PST", -8*60*60)
	for _, tc := range []struct {
		name          string
		at            time.Time
		in, out, read float64
	}{
		{"introductory", time.Date(2026, time.October, 30, 12, 0, 0, 0, time.UTC), 2, 10, 0.10},
		{"last intro minute (Pacific)", time.Date(2026, time.November, 30, 23, 59, 0, 0, pt), 2, 10, 0.10},
		{"standard from Dec 1 (Pacific)", time.Date(2026, time.December, 1, 0, 0, 0, 0, pt), 4, 20, 0.20},
		{"standard later", time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC), 4, 20, 0.20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withClock(t, tc.at)
			in, out, known := ListPrice("GOOGLEAI", "gemini-4-argon")
			if !known || !near(in, tc.in) || !near(out, tc.out) {
				t.Fatalf("list price %v/%v known=%v, want %v/%v", in, out, known, tc.in, tc.out)
			}
			write, read := CacheRates("GOOGLEAI", "gemini-4-argon")
			if write != 0 || !near(read, tc.read) {
				t.Fatalf("cache rates write=%v read=%v, want 0 / %v", write, read, tc.read)
			}
		})
	}
}

// One call end to end, Gemini usage shape: cached tokens are a subset of
// the prompt, thinking tokens come outside the candidates count and bill
// as output.
func TestGeminiArgon_CallCost(t *testing.T) {
	withClock(t, time.Date(2026, time.October, 30, 12, 0, 0, 0, time.UTC))
	u := &models.UsageInfo{
		PromptTokens:         1_000_000,
		CacheReadInputTokens: 400_000,
		CompletionTokens:     100_000,
		ReasoningTokens:      50_000,
	}
	c := CostOf("GOOGLEAI", "gemini-4-argon", u)
	// 600K fresh input at $2, 400K cached at $0.10, 150K output at $10.
	want := 0.6*2 + 0.4*0.10 + 0.15*10
	if !c.Known || !near(c.TotalUSD, want) {
		t.Fatalf("call cost %+v, want total %v", c, want)
	}
	// No long-context tier: a prompt past 200K is not re-priced.
	if in, out := LongContextMultipliers("GOOGLEAI", "gemini-4-argon", 900_000); in != 1 || out != 1 {
		t.Fatalf("argon has no long-context tier: %v/%v", in, out)
	}
}
