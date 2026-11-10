/*
 * ChatCLI - Tests for the Nov 2026 catalog refresh
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fable 5.2 (Nov 9 2026): its own entry on every provider that serves it,
// ahead of the older Fables whose ids are prefixes of it, and the owner of
// the bare "fable" shortcut.
func TestNov2026_Fable52(t *testing.T) {
	for _, id := range []string{"claude-fable-5-2", "fable-5-2", "claude-fable-5.2", "fable-5.2", "fable"} {
		meta, ok := Resolve(ProviderClaudeAI, id)
		require.True(t, ok, id)
		assert.Equal(t, "claude-fable-5-2", meta.ID, id)
		assert.Equal(t, 1000000, meta.ContextWindow)
		assert.Equal(t, 128000, meta.MaxOutputTokens)
	}
	for _, capability := range []string{"vision", "tools", "adaptive_thinking", "output_effort", "task_budget", "fast_mode", "mid_conversation_system"} {
		assert.True(t, HasCapability(ProviderClaudeAI, "claude-fable-5-2", capability), capability)
	}
	// The older entries keep their own ids.
	for id, want := range map[string]string{"claude-fable-5-1": "claude-fable-5-1", "fable-5.1": "claude-fable-5-1", "claude-fable-5": "claude-fable-5", "fable-5": "claude-fable-5"} {
		meta, ok := Resolve(ProviderClaudeAI, id)
		require.True(t, ok, id)
		assert.Equal(t, want, meta.ID, id)
	}

	for provider, ids := range map[string]map[string]string{
		ProviderBedrock: {
			"global.anthropic.claude-fable-5-2": "global.anthropic.claude-fable-5-2",
			"us.anthropic.claude-fable-5-2":     "global.anthropic.claude-fable-5-2",
			"anthropic.claude-fable-5-2":        "global.anthropic.claude-fable-5-2",
			"claude-fable-5-2":                  "global.anthropic.claude-fable-5-2",
			"anthropic.claude-fable-5-1":        "anthropic.claude-fable-5-1",
			"anthropic.claude-fable-5":          "anthropic.claude-fable-5",
		},
		ProviderOpenRouter: {
			"anthropic/claude-fable-5.2": "anthropic/claude-fable-5.2",
			"anthropic/claude-fable-5.1": "anthropic/claude-fable-5.1",
			"anthropic/claude-fable-5":   "anthropic/claude-fable-5",
		},
		ProviderCopilot: {"claude-fable-5.2": "claude-fable-5.2", "claude-fable-5-2": "claude-fable-5.2"},
		ProviderDevin: {
			"claude-fable-5.2":      "claude-fable-5.2",
			"claude-fable-5-2-high": "claude-fable-5.2",
			"claude-fable-5.1":      "claude-fable-5.1",
			"claude-fable-5":        "claude-fable-5",
		},
	} {
		for id, want := range ids {
			meta, ok := Resolve(provider, id)
			require.True(t, ok, "%s %s", provider, id)
			assert.Equal(t, want, meta.ID, "%s %s", provider, id)
		}
	}
	// On Bedrock the model card lists bedrock-runtime only: unlike Fable 5
	// and 5.1 it must not be routed to the Messages (Mantle) endpoint.
	assert.False(t, HasCapability(ProviderBedrock, "global.anthropic.claude-fable-5-2", "bedrock_mantle_only"))
	assert.True(t, HasCapability(ProviderBedrock, "anthropic.claude-fable-5-1", "bedrock_mantle_only"))
}

// The other models of the refresh, each under the id its provider serves,
// ahead of the sibling whose id or alias is a prefix of it.
func TestNov2026_NewModels(t *testing.T) {
	for _, tc := range []struct {
		provider, id, want string
		ctx, out           int
	}{
		{ProviderGoogleAI, "gemini-4-carbon", "gemini-4-carbon", 1048576, 65536},
		{ProviderGoogleAI, "gemini-4-argon", "gemini-4-argon", 1048576, 65536},
		{ProviderOpenRouter, "google/gemini-4-carbon", "google/gemini-4-carbon", 1048576, 65536},
		{ProviderOpenRouter, "google/gemini-4-argon", "google/gemini-4-argon", 1048576, 65536},
		{ProviderCopilot, "gemini-4-argon", "gemini-4-argon", 1048576, 65536},

		{ProviderOpenAI, "gpt-6.1-terra", "gpt-6.1-terra", 1050000, 128000},
		{ProviderOpenAI, "gpt-6.1-terra-2026-10-26", "gpt-6.1-terra", 1050000, 128000},
		{ProviderOpenAI, "gpt-6.1-sol", "gpt-6.1-sol", 1050000, 128000},
		{ProviderOpenAI, "gpt-6.1", "gpt-6.1-sol", 1050000, 128000},
		{ProviderCopilot, "gpt-6.1-terra", "gpt-6.1-terra", 1050000, 128000},
		{ProviderOpenRouter, "openai/gpt-6.1-terra", "openai/gpt-6.1-terra", 1050000, 128000},
		{ProviderBedrock, "global.openai.gpt-6.1-terra", "global.openai.gpt-6.1-terra", 1050000, 128000},
		{ProviderBedrock, "us.openai.gpt-6.1-terra", "global.openai.gpt-6.1-terra", 1050000, 128000},
		{ProviderBedrock, "global.openai.gpt-6.1-sol", "global.openai.gpt-6.1-sol", 1050000, 131072},
		{ProviderDevin, "gpt-6-1-terra-medium", "gpt-6.1-terra", 1050000, 128000},

		{ProviderMoonshot, "kimi-k3.1-pro", "kimi-k3.1-pro", 1048576, 131072},
		{ProviderMoonshot, "kimi-k3.1", "kimi-k3.1-pro", 1048576, 131072},
		{ProviderMoonshot, "kimi-k3.1-pro-highspeed", "kimi-k3.1-pro-highspeed", 1048576, 131072},
		{ProviderMoonshot, "kimi-k3", "kimi-k3", 1048576, 131072},
		{ProviderMoonshot, "k3", "kimi-k3", 1048576, 131072},
		{ProviderOpenRouter, "moonshotai/kimi-k3.1-pro", "moonshotai/kimi-k3.1-pro", 1048576, 131072},
		{ProviderBedrock, "global.moonshotai.kimi-k3.1", "global.moonshotai.kimi-k3.1", 1048576, 131072},
		{ProviderBedrock, "us.moonshotai.kimi-k3.1", "global.moonshotai.kimi-k3.1", 1048576, 131072},
		{ProviderBedrock, "global.moonshotai.kimi-k3", "global.moonshotai.kimi-k3", 1048576, 131072},
		{ProviderDevin, "kimi-k3-1-pro-high", "kimi-k3.1-pro", 1048576, 131072},
		{ProviderDevin, "kimi-k3", "kimi-k3", 1048576, 131072},

		{ProviderMiniMax, "MiniMax-M3.1-Flash", "MiniMax-M3.1-Flash", 1000000, 131072},
		{ProviderMiniMax, "minimax-m3.1-flash", "MiniMax-M3.1-Flash", 1000000, 131072},
		{ProviderMiniMax, "MiniMax-M3", "MiniMax-M3", 1048576, 131072},
		{ProviderMiniMax, "m3", "MiniMax-M3", 1048576, 131072},
		{ProviderOpenRouter, "minimax/minimax-m3.1-flash", "minimax/minimax-m3.1-flash", 1048576, 131072},

		{ProviderZAI, "glm-5.3-flashx-legal", "glm-5.3-flashx-legal", 1000000, 128000},
		{ProviderZAI, "glm-5.3-flashx", "glm-5.3-flashx", 1000000, 128000},

		{ProviderDevin, "swe-2-flash-high", "swe-2-flash", 200000, 32000},
		{ProviderDevin, "swe-2-max", "swe-2", 200000, 32000},
		{ProviderDevin, "claude-sonnet-5-5-high", "claude-sonnet-5.5", 1000000, 128000},
		{ProviderDevin, "claude-sonnet-5", "claude-sonnet-5", 1000000, 128000},
		{ProviderDevin, "gemini-4-argon-high", "gemini-4-argon", 1048576, 65536},
		{ProviderDevin, "grok-4-7-high", "grok-4.7", 500000, 16384},
	} {
		meta, ok := Resolve(tc.provider, tc.id)
		if !assert.True(t, ok, "%s %s", tc.provider, tc.id) {
			continue
		}
		assert.Equal(t, tc.want, meta.ID, "%s %s", tc.provider, tc.id)
		assert.Equal(t, tc.ctx, meta.ContextWindow, "%s %s context", tc.provider, tc.id)
		assert.Equal(t, tc.out, meta.MaxOutputTokens, "%s %s output", tc.provider, tc.id)
	}
	// Kimi K3.1 Pro always thinks: it must not advertise the on/off toggle
	// MOONSHOT_THINKING drives.
	assert.False(t, HasCapability(ProviderMoonshot, "kimi-k3.1-pro", "thinking"))
	assert.True(t, HasCapability(ProviderMoonshot, "kimi-k3", "thinking"))
	// Carbon is implicit-cache only, like Argon.
	assert.True(t, HasCapability(ProviderGoogleAI, "gemini-4-carbon", CapabilityNoExplicitCache))
}

// A display name is earned by an exact id or alias. An id the catalog does
// not list still borrows a sibling's limits, but is shown as itself — the
// release after Fable 5.2 must not sign its replies "Claude Fable 5.2".
func TestGetDisplayName_ExactMatchOnly(t *testing.T) {
	assert.Equal(t, "Claude Fable 5.2 (1M context)", GetDisplayName(ProviderClaudeAI, "claude-fable-5-2"))
	assert.Equal(t, "Claude Fable 5.2 (1M context)", GetDisplayName(ProviderClaudeAI, "fable"))
	assert.Equal(t, "Claude Fable 5.1 (1M context)", GetDisplayName(ProviderClaudeAI, "claude-fable-5-1"))
	assert.Equal(t, "Gemini 4 Carbon", GetDisplayName(ProviderGoogleAI, "gemini-4-carbon"))

	for provider, id := range map[string]string{
		ProviderClaudeAI: "claude-fable-5-3",
		ProviderOpenAI:   "gpt-6.2-sol",
		ProviderGoogleAI: "gemini-4-carbon-preview",
		ProviderMoonshot: "kimi-k3.2-pro",
	} {
		meta, ok := Resolve(provider, id)
		require.True(t, ok, "%s still borrows a sibling's limits", id)
		assert.NotEqual(t, id, meta.ID)
		assert.Equal(t, id, GetDisplayName(provider, id), "%s must be shown as itself", id)
	}
	assert.Equal(t, "no-such-model", GetDisplayName(ProviderOpenAI, "no-such-model"))
}

// The loose pass picks the longest catalog name the requested id contains,
// not the first one in registry order, and a name ending in a digit does
// not match in the middle of a longer number. Every row below resolved to
// the wrong sibling before (the "was" column).
func TestResolve_LooseMatchPrefersTheMostSpecificName(t *testing.T) {
	for _, tc := range []struct{ provider, id, want, was string }{
		// The bare "fable" shortcut of the newest Fable must not capture a
		// dated id of an older one.
		{ProviderClaudeAI, "claude-fable-5-1-20260901", "claude-fable-5-1", "claude-fable-5-2"},
		{ProviderClaudeAI, "claude-fable-5-latest", "claude-fable-5", "claude-fable-5-1"},
		// A date stamp starting with "2" is not version 5.2.
		{ProviderClaudeAI, "claude-fable-5-20260609", "claude-fable-5", "claude-fable-5-2"},
		{ProviderBedrock, "anthropic.claude-fable-5-20260609", "anthropic.claude-fable-5", "global.anthropic.claude-fable-5-2"},
		// Astra's "gpt-6" family alias used to swallow dated Sol and Luna
		// ids — a $10/$50 price on a $2/$10 model.
		{ProviderOpenAI, "gpt-6-sol-2026-09-22", "gpt-6-sol", "gpt-6-astra"},
		{ProviderOpenAI, "gpt-6-luna-2026-09-22", "gpt-6-luna", "gpt-6-astra"},
		{ProviderOpenAI, "gpt-4o-mini-2024-07-18", "gpt-4o-mini", "gpt-4o"},
		{ProviderZAI, "glm-5-20260101", "glm-5", "glm-5.2"},
		// A profile id longer than its entry's aliases resolves by the id.
		{ProviderBedrock, "global.anthropic.claude-sonnet-5-5-20260928", "global.anthropic.claude-sonnet-5-5", "anthropic.claude-sonnet-5"},
		// An unlisted release still borrows a sibling's limits: the one
		// whose name it carries, or the family shortcut's.
		{ProviderClaudeAI, "claude-fable-5-3", "claude-fable-5", ""},
		{ProviderClaudeAI, "claude-fable-6", "claude-fable-5-2", ""},
		{ProviderOpenAI, "gpt-6.1-luna", "gpt-6.1-sol", ""},
	} {
		meta, ok := Resolve(tc.provider, tc.id)
		require.True(t, ok, tc.id)
		assert.Equal(t, tc.want, meta.ID, "%s %s", tc.provider, tc.id)
	}
}

func TestContainsName(t *testing.T) {
	assert.True(t, containsName("claude-fable-5-2-20261109", "claude-fable-5-2"))
	assert.True(t, containsName("claude-fable-5-2", "claude-fable-5-2"))
	assert.False(t, containsName("claude-fable-5-20260609", "claude-fable-5-2"))
	assert.True(t, containsName("claude-fable-5-20260609", "claude-fable-5"))
	assert.True(t, containsName("gpt-4o-mini", "gpt-4o"))
	// A later occurrence can still match after a rejected one.
	assert.True(t, containsName("m-25-m-2", "m-2"))
	assert.False(t, containsName("anything", ""))
}

// Models the vendors stopped serving are gone from the registry: OpenAI
// shut down o3-mini, o4-mini and gpt-4.1-nano on Oct 23 2026, Bedrock's
// Claude Sonnet 4 reached EOL on Oct 14 2026, and OpenRouter answers 404
// "no longer available" for the three slugs below.
func TestNov2026_RetiredModelsLeftTheRegistry(t *testing.T) {
	for provider, ids := range map[string][]string{
		ProviderOpenAI:     {"o3-mini", "o4-mini", "gpt-4.1-nano"},
		ProviderOpenRouter: {"anthropic/claude-sonnet-4", "anthropic/claude-opus-4", "deepseek/deepseek-r1"},
		ProviderBedrock: {
			"global.anthropic.claude-sonnet-4-20250514-v1:0",
			"us.anthropic.claude-sonnet-4-20250514-v1:0",
			"eu.anthropic.claude-sonnet-4-20250514-v1:0",
		},
	} {
		for _, id := range ids {
			_, ok := ResolveExact(provider, id)
			assert.False(t, ok, "%s %s was retired and must not be listed", provider, id)
			assert.Equal(t, id, GetDisplayName(provider, id), "%s is shown as itself", id)
		}
	}
	// Their neighbours stay.
	for provider, id := range map[string]string{
		ProviderOpenAI:     "o3",
		ProviderOpenRouter: "anthropic/claude-sonnet-5",
		ProviderBedrock:    "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
	} {
		_, ok := ResolveExact(provider, id)
		assert.True(t, ok, "%s %s", provider, id)
	}
}
