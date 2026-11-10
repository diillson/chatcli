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
		// Families Cognition's public table priced by Nov 9 2026.
		{"claude-fable-5-2-high", 8, 40, true},
		{"claude-fable-5-2-high-fast", 16, 80, true},
		{"claude-fable-5-1", 10, 50, true},
		{"claude-5-fable-high", 10, 50, true},
		{"claude-sonnet-5-5-low", 2, 10, true},
		{"claude-sonnet-5", 2, 10, true},
		{"claude-haiku-5-5-high", 0.1, 0.5, true},
		{"gpt-6-1-terra-medium", 1, 5, true},
		{"gpt-6-1-terra-medium-priority", 2, 10, true},
		{"gpt-6-1-sol-high", 2, 10, true},
		{"gpt-6-astra-max-priority", 20, 100, true},
		{"gpt-6-luna", 0.1, 0.5, true},
		{"gpt-5.6-sol", 4, 20, true},
		{"kimi-k3-1-pro-high", 2, 10, true},
		{"kimi-k3", 3, 15, true},
		{"grok-4.7", 2, 6, true},
		// Never listed with a stable price: unknown, not guessed.
		{"swe-2-flash-high", 0, 0, false},
		{"gemini-4-argon-high", 0, 0, false},
		{"adaptive", 0, 0, false},
		{"", 0, 0, false},
	} {
		in, out, ok := DevinStaticRate(tc.model)
		assert.Equal(t, tc.ok, ok, "%q ok", tc.model)
		assert.InDelta(t, tc.in, in, 1e-9, "%q in", tc.model)
		assert.InDelta(t, tc.out, out, 1e-9, "%q out", tc.model)
	}
}
