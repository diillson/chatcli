/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaxTokensEnvVar_KnowsEveryProviderWithAnOverride(t *testing.T) {
	for _, p := range []string{ProviderOpenAI, ProviderClaudeAI, ProviderGoogleAI, ProviderXAI, ProviderZAI,
		ProviderMiniMax, ProviderMoonshot, ProviderOllama, ProviderStackSpot, ProviderCopilot, ProviderBedrock, ProviderOpenRouter} {
		name, ok := MaxTokensEnvVar(p)
		assert.True(t, ok, p)
		assert.Contains(t, name, "_MAX_TOKENS", p)
	}
	_, ok := MaxTokensEnvVar("SOMETHING_ELSE")
	assert.False(t, ok)
	name, ok := MaxTokensEnvVar(" claudeai ")
	assert.True(t, ok, "case and whitespace insensitive")
	assert.Equal(t, "ANTHROPIC_MAX_TOKENS", name)
}

func TestMaxTokensEnvOverride_ParsesOnlyPositiveIntegers(t *testing.T) {
	t.Setenv("OPENAI_MAX_TOKENS", "4096")
	assert.Equal(t, 4096, MaxTokensEnvOverride("OPENAI"))
	t.Setenv("OPENAI_MAX_TOKENS", " 512 ")
	assert.Equal(t, 512, MaxTokensEnvOverride("openai"))
	for _, bad := range []string{"", "abc", "0", "-5", "1.5"} {
		t.Setenv("OPENAI_MAX_TOKENS", bad)
		assert.Equal(t, 0, MaxTokensEnvOverride("OPENAI"), "value %q", bad)
	}
	assert.Equal(t, 0, MaxTokensEnvOverride("UNKNOWN"))
}

func TestEffectiveMaxTokens_Precedence(t *testing.T) {
	t.Setenv("ANTHROPIC_MAX_TOKENS", "")
	catalogDefault := GetMaxTokens(ProviderClaudeAI, "claude-sonnet-4-6", 0)
	assert.Greater(t, catalogDefault, 0)
	assert.Equal(t, 777, EffectiveMaxTokens(ProviderClaudeAI, "claude-sonnet-4-6", 777), "explicit request wins")
	assert.Equal(t, catalogDefault, EffectiveMaxTokens(ProviderClaudeAI, "claude-sonnet-4-6", 0), "catalog when nothing else")
	t.Setenv("ANTHROPIC_MAX_TOKENS", "2048")
	assert.Equal(t, 2048, EffectiveMaxTokens(ProviderClaudeAI, "claude-sonnet-4-6", 0), "env override beats the catalog")
	assert.Equal(t, 9, EffectiveMaxTokens(ProviderClaudeAI, "claude-sonnet-4-6", 9), "explicit request beats the env")
}
