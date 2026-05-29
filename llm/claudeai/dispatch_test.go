/*
 * ChatCLI - Tests for per-turn dispatch helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/stretchr/testify/assert"
)

// usesAdaptiveThinkingOnly and applyThinkingForEffort drive the per-turn
// thinking dispatch on every Anthropic call. The capability lookup is the
// production source of truth for which models accept budgeted thinking vs
// adaptive thinking — a silent registry drift here would either trip a
// 400 from the API (sending budget_tokens to Opus 4.7+) or silently
// disable thinking on the older 4.x line. These tests pin both branches
// and the no-op branches.

func TestUsesAdaptiveThinkingOnly(t *testing.T) {
	// Opus 4.7 and 4.8 advertise adaptive_thinking in the catalog
	// (capability flag, see llm/catalog/catalog.go).
	for _, model := range []string{"claude-opus-4-8", "claude-opus-4-7"} {
		assert.True(t, usesAdaptiveThinkingOnly(model),
			"%s must route through adaptive thinking — budget_tokens is rejected with 400", model)
	}
	// Older 4.x and 3.x do NOT advertise the capability — they accept
	// budgeted extended thinking via the legacy path.
	for _, model := range []string{
		"claude-opus-4-5", "claude-opus-4-1-20250805",
		"claude-sonnet-4-5", "claude-sonnet-3-7-20250219",
	} {
		assert.False(t, usesAdaptiveThinkingOnly(model),
			"%s must keep using budgeted extended thinking", model)
	}
}

func TestApplyThinkingForEffort(t *testing.T) {
	cases := []struct {
		name        string
		model       string
		effort      client.SkillEffort
		initialMax  int
		wantApplied bool
		wantThink   map[string]interface{}
		wantMax     int // expected max_tokens after dispatch
	}{
		{
			name:        "unset effort emits nothing",
			model:       "claude-opus-4-8",
			effort:      client.EffortUnset,
			initialMax:  4096,
			wantApplied: false,
			wantMax:     4096,
		},
		{
			name:        "adaptive-only model emits adaptive thinking",
			model:       "claude-opus-4-8",
			effort:      client.EffortHigh,
			initialMax:  4096,
			wantApplied: true,
			wantThink:   map[string]interface{}{"type": "adaptive"},
			wantMax:     4096, // adaptive must NOT raise max_tokens (no budget)
		},
		{
			name:        "adaptive-only model with low effort still emits adaptive",
			model:       "claude-opus-4-7",
			effort:      client.EffortLow,
			initialMax:  4096,
			wantApplied: true,
			wantThink:   map[string]interface{}{"type": "adaptive"},
			wantMax:     4096,
		},
		{
			name:        "budgeted model with high effort emits enabled+budget and raises max_tokens",
			model:       "claude-sonnet-4-5",
			effort:      client.EffortHigh,
			initialMax:  4096,
			wantApplied: true,
			wantThink: map[string]interface{}{
				"type":          "enabled",
				"budget_tokens": 16384,
			},
			wantMax: 16384 + 1024, // budget+1024 floor
		},
		{
			name:        "budgeted model already with sufficient max_tokens preserves it",
			model:       "claude-sonnet-4-5",
			effort:      client.EffortMedium,
			initialMax:  64000,
			wantApplied: true,
			wantThink: map[string]interface{}{
				"type":          "enabled",
				"budget_tokens": 4096,
			},
			wantMax: 64000, // already > 4096+1024, do not lower
		},
		{
			name:        "low effort yields no budget and no thinking on budgeted models",
			model:       "claude-sonnet-4-5",
			effort:      client.EffortLow,
			initialMax:  4096,
			wantApplied: false,
			wantMax:     4096,
		},
		{
			name:        "unknown model with effort is a no-op",
			model:       "claude-haiku-3-5-20241022",
			effort:      client.EffortHigh,
			initialMax:  4096,
			wantApplied: false,
			wantMax:     4096,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqBody := map[string]interface{}{"max_tokens": tc.initialMax}
			ctx := context.Background()
			if tc.effort != client.EffortUnset {
				ctx = client.WithEffortHint(ctx, tc.effort)
			}

			applied := applyThinkingForEffort(reqBody, tc.model, ctx)
			assert.Equal(t, tc.wantApplied, applied, "applied flag")
			assert.Equal(t, tc.wantMax, reqBody["max_tokens"], "max_tokens after dispatch")

			if tc.wantApplied {
				assert.Equal(t, tc.wantThink, reqBody["thinking"])
			} else {
				_, has := reqBody["thinking"]
				assert.False(t, has, "thinking block must NOT be present")
			}
		})
	}
}

func TestApplyFastModeIfRequested(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		envValue  string
		wantApply bool
	}{
		{"env unset is no-op", "claude-opus-4-8", "", false},
		{"env=fast on fast_mode model applies", "claude-opus-4-8", "fast", true},
		{"env=FAST is case-insensitive", "claude-opus-4-8", "FAST", true},
		{"env=fast with surrounding space still applies", "claude-opus-4-8", "  fast  ", true},
		{"env=fast on non-fast_mode model is silent no-op", "claude-opus-4-7", "fast", false},
		{"env=fast on older model is silent no-op", "claude-opus-4-5", "fast", false},
		{"env=anything-else is no-op", "claude-opus-4-8", "turbo", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_SPEED", tc.envValue)
			reqBody := map[string]interface{}{}
			applied := applyFastModeIfRequested(reqBody, tc.model)
			assert.Equal(t, tc.wantApply, applied)
			if tc.wantApply {
				assert.Equal(t, "fast", reqBody["speed"])
			} else {
				_, has := reqBody["speed"]
				assert.False(t, has, "speed must NOT leak onto the payload")
			}
		})
	}
}
