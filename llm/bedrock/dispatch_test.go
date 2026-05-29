/*
 * ChatCLI - Tests for Bedrock per-turn dispatch helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/stretchr/testify/assert"
)

// applyAnthropicThinkingForEffort mirrors the claudeai-side dispatcher
// against the Bedrock catalog mirrors. Both share the catalog
// "adaptive_thinking" capability flag so adding new adaptive-only models
// stays a registry-only change. These tests pin every branch of the
// dispatch and the side-effect on max_tokens.

func TestApplyAnthropicThinkingForEffort(t *testing.T) {
	cases := []struct {
		name        string
		model       string
		effort      client.SkillEffort
		initialMax  int
		wantApplied bool
		wantThink   map[string]interface{}
		wantMax     int
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
			name:        "adaptive-only Bedrock model emits adaptive",
			model:       "claude-opus-4-8",
			effort:      client.EffortHigh,
			initialMax:  4096,
			wantApplied: true,
			wantThink:   map[string]interface{}{"type": "adaptive"},
			wantMax:     4096,
		},
		{
			name:        "adaptive-only via Bedrock inference profile ID resolves through alias",
			model:       "global.anthropic.claude-opus-4-7-20260401-v1:0",
			effort:      client.EffortMedium,
			initialMax:  4096,
			wantApplied: true,
			wantThink:   map[string]interface{}{"type": "adaptive"},
			wantMax:     4096,
		},
		{
			name:        "budgeted Bedrock model emits enabled+budget and raises max_tokens",
			model:       "us.anthropic.claude-sonnet-4-20250514-v1:0",
			effort:      client.EffortHigh,
			initialMax:  4096,
			wantApplied: true,
			wantThink: map[string]interface{}{
				"type":          "enabled",
				"budget_tokens": 16384,
			},
			wantMax: 16384 + 1024,
		},
		{
			name:        "budgeted Bedrock model with sufficient max_tokens preserves it",
			model:       "us.anthropic.claude-sonnet-4-20250514-v1:0",
			effort:      client.EffortMedium,
			initialMax:  64000,
			wantApplied: true,
			wantThink: map[string]interface{}{
				"type":          "enabled",
				"budget_tokens": 4096,
			},
			wantMax: 64000,
		},
		{
			name:        "low effort yields no budget and no thinking",
			model:       "us.anthropic.claude-sonnet-4-20250514-v1:0",
			effort:      client.EffortLow,
			initialMax:  4096,
			wantApplied: false,
			wantMax:     4096,
		},
		{
			name:        "non-thinking-capable Bedrock model is a no-op",
			model:       "anthropic.claude-3-haiku-20240307-v1:0",
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

			applied := applyAnthropicThinkingForEffort(reqBody, tc.model, ctx)
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

// TestSupportsExtendedThinking_BedrockProfiles pins that the substring
// check covers Bedrock inference-profile-prefixed IDs alongside the bare
// Claude API IDs. supportsExtendedThinking is the second-tier gate in
// the budgeted-thinking path, so a regression here silently disables
// extended thinking on legacy Bedrock callers.
func TestSupportsExtendedThinking_BedrockProfiles(t *testing.T) {
	yes := []string{
		"claude-opus-4-5",
		"global.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"us.anthropic.claude-opus-4-20250514-v1:0",
		"eu.anthropic.claude-3-7-sonnet-20250219-v1:0",
	}
	for _, m := range yes {
		assert.True(t, supportsExtendedThinking(m), "%s should be treated as thinking-capable", m)
	}

	no := []string{
		"anthropic.claude-3-haiku-20240307-v1:0",
		"openai.gpt-oss-120b-1:0",
		"meta.llama3-70b-instruct-v1:0",
	}
	for _, m := range no {
		assert.False(t, supportsExtendedThinking(m), "%s must NOT be treated as thinking-capable", m)
	}
}
