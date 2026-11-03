/*
 * ChatCLI - Tests for Gemini 3.6/3.7/3.8 Flash dated pricing
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pricing

import (
	"testing"
	"time"
)

// ai.google.dev/gemini-api/docs/pricing (Nov 2026): $0.75/$3.75 with cached
// input at $0.075 "through December 31, 2026"; $1.50/$7.50 and $0.15 from
// "January 1, 2027". gemini-3.7-flash is routed to 3.8 Flash and bills the
// same.
func TestGeminiFlash_DatedPrice(t *testing.T) {
	pt := time.FixedZone("PST", -8*60*60)
	for _, tc := range []struct {
		name          string
		at            time.Time
		in, out, read float64
	}{
		{"lower price", time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC), 0.75, 3.75, 0.075},
		{"last minute of 2026 (Pacific)", time.Date(2026, time.December, 31, 23, 59, 0, 0, pt), 0.75, 3.75, 0.075},
		{"standard from Jan 1 2027 (Pacific)", time.Date(2027, time.January, 1, 0, 0, 0, 0, pt), 1.50, 7.50, 0.15},
		{"standard later", time.Date(2027, time.June, 1, 0, 0, 0, 0, time.UTC), 1.50, 7.50, 0.15},
	} {
		for _, model := range []string{"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.8-flash-latest"} {
			t.Run(tc.name+"/"+model, func(t *testing.T) {
				withClock(t, tc.at)
				in, out, known := ListPrice("GOOGLEAI", model)
				if !known || !near(in, tc.in) || !near(out, tc.out) {
					t.Fatalf("list price %v/%v known=%v, want %v/%v", in, out, known, tc.in, tc.out)
				}
				write, read := CacheRates("GOOGLEAI", model)
				if write != 0 || !near(read, tc.read) {
					t.Fatalf("cache rates write=%v read=%v, want 0 / %v", write, read, tc.read)
				}
			})
		}
	}
}

// The other Gemini Flash tiers keep their fixed price across the flip.
func TestGeminiFlash_FlipLeavesOtherTiers(t *testing.T) {
	withClock(t, time.Date(2027, time.February, 1, 0, 0, 0, 0, time.UTC))
	for model, want := range map[string][2]float64{
		"gemini-3.5-flash":      {1.50, 9.0},
		"gemini-3.5-flash-lite": {0.30, 2.50},
		"gemini-3.1-flash-lite": {0.25, 1.50},
		"gemini-3-flash":        {0.50, 3.0},
	} {
		in, out, _ := ListPrice("GOOGLEAI", model)
		if !near(in, want[0]) || !near(out, want[1]) {
			t.Fatalf("%s: %v/%v, want %v/%v", model, in, out, want[0], want[1])
		}
	}
}
