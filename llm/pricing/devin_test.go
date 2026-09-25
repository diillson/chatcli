/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pricing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDevinStaticRate(t *testing.T) {
	type dc struct {
		model   string
		in, out float64
		ok      bool
	}
	for _, tc := range []dc{
		// Dotted slug and hyphenated uid hit the same row.
		{"claude-opus-4.8", 5, 25, true},
		{"claude-opus-4-8-medium", 5, 25, true},
		// Fast and priority suffixes take the fast rate where one exists.
		{"claude-opus-4-8-medium-fast", 10, 50, true},
		{"gpt-5.6-sol-priority", 8, 40, true},
		// A family without a fast variant keeps the base rate on any suffix.
		{"claude-sonnet-4-6-high-fast", 3, 15, true},
		// Longest key wins: mini never resolves as the base tier.
		{"gpt-5.4-mini", 0.75, 4.5, true},
		{"gpt-5.4", 2.5, 15, true},
		// Opus 5.5 has its own row above Opus 5.
		{"claude-opus-5-5", 4, 20, true},
		{"claude-opus-5-5-fast", 8, 40, true},
		{"claude-opus-5", 5, 25, true},
		// A longer id must continue with a separator.
		{"gpt-5.5x", 0, 0, false},
		{"gpt-5.5-medium", 5, 30, true},
		// Cognition in-house families.
		{"swe-1.6-fast", 0.5, 2.5, true},
		{"swe-1.7-lightning", 2.5, 12.5, true},
		{"  SWE-1.7  ", 0.5, 2.5, true},
		// Never listed with a price: unknown, not guessed.
		{"claude-fable-5-1", 0, 0, false},
		{"grok-4.7", 0, 0, false},
		{"", 0, 0, false},
	} {
		in, out, ok := DevinStaticRate(tc.model)
		assert.Equal(t, tc.ok, ok, "%q ok", tc.model)
		assert.InDelta(t, tc.in, in, 1e-9, "%q in", tc.model)
		assert.InDelta(t, tc.out, out, 1e-9, "%q out", tc.model)
	}
}
