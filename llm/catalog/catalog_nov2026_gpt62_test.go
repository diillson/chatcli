/*
 * ChatCLI - Tests for the mid-Nov 2026 catalog refresh
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GPT-6.2 Astra and Sol (Nov 17 2026), Gemini 4 Neon (Nov 18) and Hydrogen
// (Nov 12): each under the id its provider serves, with its own limits.
func TestNov2026b_NewModels(t *testing.T) {
	for _, tc := range []struct {
		provider, id, want string
		ctx, out           int
	}{
		{ProviderOpenAI, "gpt-6.2-astra", "gpt-6.2-astra", 1050000, 128000},
		{ProviderOpenAI, "gpt-6-2-astra", "gpt-6.2-astra", 1050000, 128000},
		{ProviderOpenAI, "gpt-6.2-sol", "gpt-6.2-sol", 1050000, 128000},
		{ProviderOpenAI, "gpt-6.2", "gpt-6.2-sol", 1050000, 128000},
		{ProviderOpenAI, "gpt-6.2-sol-2026-11-17", "gpt-6.2-sol", 1050000, 128000},
		{ProviderOpenAI, "gpt-6.2-astra-2026-11-17", "gpt-6.2-astra", 1050000, 128000},
		{ProviderGoogleAI, "gemini-4-neon", "gemini-4-neon", 1048576, 65536},
		{ProviderGoogleAI, "gemini-4-hydrogen", "gemini-4-hydrogen", 1048576, 65536},
		{ProviderCopilot, "gpt-6.2-astra", "gpt-6.2-astra", 1050000, 128000},
		{ProviderCopilot, "gpt-6-2-sol", "gpt-6.2-sol", 1050000, 128000},
		{ProviderCopilot, "gemini-4-neon", "gemini-4-neon", 1048576, 65536},
		{ProviderCopilot, "gemini-4-hydrogen", "gemini-4-hydrogen", 1048576, 65536},
		{ProviderCopilot, "gemini-4-carbon", "gemini-4-carbon", 1048576, 32768},
		{ProviderOpenRouter, "openai/gpt-6.2-astra", "openai/gpt-6.2-astra", 1050000, 128000},
		{ProviderOpenRouter, "openai/gpt-6.2-sol", "openai/gpt-6.2-sol", 1050000, 128000},
		{ProviderOpenRouter, "google/gemini-4-neon", "google/gemini-4-neon", 1048576, 65536},
		{ProviderOpenRouter, "google/gemini-4-hydrogen", "google/gemini-4-hydrogen", 1048576, 65536},
		{ProviderBedrock, "global.openai.gpt-6.2-astra", "global.openai.gpt-6.2-astra", 1050000, 128000},
		{ProviderBedrock, "us.openai.gpt-6.2-sol", "global.openai.gpt-6.2-sol", 1050000, 128000},
		{ProviderBedrock, "openai.gpt-6.2-sol", "global.openai.gpt-6.2-sol", 1050000, 128000},
		{ProviderDevin, "gemini-4-neon-xhigh", "gemini-4-neon", 1048576, 65536},
		{ProviderDevin, "deepseek-v4-1-flash-1017-high", "deepseek-v4.1-flash", 1000000, 32000},
	} {
		meta, ok := Resolve(tc.provider, tc.id)
		require.True(t, ok, "%s %s", tc.provider, tc.id)
		assert.Equal(t, tc.want, meta.ID, "%s %s", tc.provider, tc.id)
		assert.Equal(t, tc.ctx, meta.ContextWindow, "%s %s context", tc.provider, tc.id)
		assert.Equal(t, tc.out, meta.MaxOutputTokens, "%s %s output", tc.provider, tc.id)
	}

	// The siblings whose names are prefixes of the new ids keep their own
	// entries, and the family shorthands do not move.
	for id, want := range map[string]string{
		"gpt-6.1-sol": "gpt-6.1-sol", "gpt-6.1": "gpt-6.1-sol", "gpt-6.1-terra": "gpt-6.1-terra",
		"gpt-6-astra": "gpt-6-astra", "gpt-6": "gpt-6-astra", "gpt-6-sol": "gpt-6-sol",
	} {
		meta, ok := Resolve(ProviderOpenAI, id)
		require.True(t, ok, id)
		assert.Equal(t, want, meta.ID, id)
	}
	for _, id := range []string{"gemini-4-carbon", "gemini-4-argon"} {
		meta, ok := Resolve(ProviderGoogleAI, id)
		require.True(t, ok, id)
		assert.Equal(t, id, meta.ID)
	}

	// Tool calling on the 6.2 pair needs the Responses API.
	assert.Equal(t, APIResponses, GetPreferredAPI(ProviderOpenAI, "gpt-6.2-astra"))
	assert.Equal(t, APIResponses, GetPreferredAPI(ProviderOpenAI, "gpt-6.2-sol"))
	// On Bedrock the pair is served through Converse on the profiles.
	assert.True(t, HasCapability(ProviderBedrock, "global.openai.gpt-6.2-astra", "bedrock_converse_only"))
	assert.True(t, HasCapability(ProviderBedrock, "global.openai.gpt-6.2-sol", "bedrock_converse_only"))
	// Neon and Hydrogen serve implicit caching only, as Argon and Carbon.
	for _, id := range []string{"gemini-4-neon", "gemini-4-hydrogen"} {
		assert.True(t, HasCapability(ProviderGoogleAI, id, CapabilityNoExplicitCache), id)
		assert.True(t, HasCapability(ProviderGoogleAI, id, "vision"), id)
		assert.True(t, HasCapability(ProviderGoogleAI, id, "tools"), id)
	}
	assert.Equal(t, "GPT-6.2 Sol", GetDisplayName(ProviderOpenAI, "gpt-6.2-sol"))
	assert.Equal(t, "Gemini 4 Neon", GetDisplayName(ProviderGoogleAI, "gemini-4-neon"))
}

// The Mythos line mirrors the Fable of the same version on the Anthropic
// API and on Bedrock; the bare shortcut tracks the newest.
func TestNov2026b_Mythos(t *testing.T) {
	for id, want := range map[string]string{
		"claude-mythos-5-2": "claude-mythos-5-2", "mythos-5.2": "claude-mythos-5-2", "mythos": "claude-mythos-5-2",
		"claude-mythos-5-1": "claude-mythos-5-1", "mythos-5.1": "claude-mythos-5-1",
		"claude-mythos-5": "claude-mythos-5", "mythos-5": "claude-mythos-5",
	} {
		meta, ok := Resolve(ProviderClaudeAI, id)
		require.True(t, ok, id)
		assert.Equal(t, want, meta.ID, id)
		assert.Equal(t, 1000000, meta.ContextWindow, id)
		assert.Equal(t, 128000, meta.MaxOutputTokens, id)
		assert.Equal(t, APIAnthropicMessages, meta.PreferredAPI, id)
	}
	for _, id := range []string{"claude-mythos-5-2", "claude-mythos-5-1", "claude-mythos-5"} {
		for _, capability := range []string{"vision", "tools", "adaptive_thinking", "output_effort", "task_budget", "mid_conversation_system"} {
			assert.True(t, HasCapability(ProviderClaudeAI, id, capability), "%s %s", id, capability)
		}
	}
	assert.True(t, HasCapability(ProviderClaudeAI, "claude-mythos-5-2", "fast_mode"))
	assert.False(t, HasCapability(ProviderClaudeAI, "claude-mythos-5-1", "fast_mode"))
	// Fable keeps its own entries and shortcut.
	fable, _ := Resolve(ProviderClaudeAI, "fable")
	assert.Equal(t, "claude-fable-5-2", fable.ID)

	for id, want := range map[string]string{
		"global.anthropic.claude-mythos-5-2": "global.anthropic.claude-mythos-5-2",
		"us.anthropic.claude-mythos-5-2":     "global.anthropic.claude-mythos-5-2",
		"claude-mythos-5-2":                  "global.anthropic.claude-mythos-5-2",
		"anthropic.claude-mythos-5-1":        "anthropic.claude-mythos-5-1",
		"claude-mythos-5-1":                  "anthropic.claude-mythos-5-1",
		"anthropic.claude-mythos-5":          "anthropic.claude-mythos-5",
		"mythos-5":                           "anthropic.claude-mythos-5",
	} {
		meta, ok := Resolve(ProviderBedrock, id)
		require.True(t, ok, id)
		assert.Equal(t, want, meta.ID, id)
	}
	assert.False(t, HasCapability(ProviderBedrock, "global.anthropic.claude-mythos-5-2", "bedrock_mantle_only"))
	assert.True(t, HasCapability(ProviderBedrock, "anthropic.claude-mythos-5-1", "bedrock_mantle_only"))
	assert.True(t, HasCapability(ProviderBedrock, "anthropic.claude-mythos-5", "bedrock_mantle_only"))
	// A refused Mythos turn falls back inside the provider, like Fable.
	assert.Equal(t, "CLAUDEAI:claude-opus-5-5", RefusalSibling(ProviderClaudeAI, "claude-mythos-5-2"))
}

// Ids the vendors stopped serving leave the catalog: a request for them
// is answered "no longer available" (MiniMax) or 404 (xAI).
func TestNov2026b_Retired(t *testing.T) {
	for _, id := range []string{"MiniMax-M2.7", "MiniMax-M2.7-highspeed", "MiniMax-M2.5", "MiniMax-M2.5-highspeed", "MiniMax-Text-01", "m2.7", "text-01"} {
		_, ok := ResolveExact(ProviderMiniMax, id)
		assert.False(t, ok, id)
	}
	for _, id := range []string{"MiniMax-M3.1-Flash", "MiniMax-M3"} {
		meta, ok := Resolve(ProviderMiniMax, id)
		require.True(t, ok, id)
		assert.Equal(t, id, meta.ID)
	}
	for _, id := range []string{"grok-build-0.1", "grok-code-fast-1"} {
		_, ok := Resolve(ProviderXAI, id)
		assert.False(t, ok, id)
	}
	// grok-build-latest is the served build alias.
	build, ok := Resolve(ProviderXAI, "grok-build-latest")
	require.True(t, ok)
	assert.Equal(t, "grok-4.5", build.ID)
}
