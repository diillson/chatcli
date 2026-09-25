/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pricing

import (
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// priceCase pins one row of the static tables: provider, model, the
// expected list price and whether the engine claims to know it.
type priceCase struct {
	provider, model string
	in, out         float64
	known           bool
}

func assertPrices(t *testing.T, cases []priceCase) {
	t.Helper()
	for _, tc := range cases {
		in, out, known := ListPrice(tc.provider, tc.model)
		assert.Equal(t, tc.known, known, "%s/%s known", tc.provider, tc.model)
		assert.InDelta(t, tc.in, in, 1e-9, "%s/%s input", tc.provider, tc.model)
		assert.InDelta(t, tc.out, out, 1e-9, "%s/%s output", tc.provider, tc.model)
	}
}

func TestListPrice_ClaudeFamily(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	assertPrices(t, []priceCase{
		{"CLAUDEAI", "claude-fable-5-1", 10, 50, true},
		{"CLAUDEAI", "claude-opus-5-5", 4, 20, true},
		{"OPENROUTER", "anthropic/claude-opus-5.5", 4, 20, true},
		{"CLAUDEAI", "claude-opus-5", 5, 25, true},
		{"BEDROCK", "anthropic.claude-opus-4-8", 5, 25, true},
		{"CLAUDEAI", "claude-opus-4-5", 5, 25, true},
		{"CLAUDEAI", "claude-opus-4-1", 15, 75, true},
		{"CLAUDEAI", "claude-sonnet-5", 2, 10, true},
		{"CLAUDEAI", "claude-sonnet-4-6", 3, 15, true},
		{"CLAUDEAI", "claude-haiku-4-5", 1, 5, true},
		{"CLAUDEAI", "claude-3-haiku", 0.25, 1.25, true},
		{"CLAUDEAI", "claude-mystery", 0, 0, false},
	})
}

func TestListPrice_OpenAIFamily(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	assertPrices(t, []priceCase{
		{"OPENAI", "gpt-6-astra", 10, 50, true},
		{"OPENAI", "gpt-6-sol", 2, 10, true},
		{"OPENAI", "gpt-6-luna", 0.10, 0.50, true},
		{"OPENAI", "gpt-5.6-terra", 2, 12, true},
		{"OPENAI", "gpt-5.6-luna", 0.20, 1.20, true},
		{"OPENAI", "gpt-5.6-sol", 4, 20, true},
		{"BEDROCK", "global.openai.gpt-5.6", 4, 20, true},
		{"OPENAI", "gpt-5.5-pro", 30, 180, true},
		{"OPENAI", "gpt-5.5", 5, 30, true},
		{"OPENAI", "gpt-5.4-pro", 30, 180, true},
		{"OPENAI", "gpt-5.4-mini", 0.75, 4.50, true},
		{"OPENAI", "gpt-5.4-nano", 0.20, 1.25, true},
		{"OPENAI", "gpt-5.4", 2.50, 15, true},
		{"OPENAI", "gpt-5.3-codex", 1.75, 14, true},
		{"OPENAI", "gpt-5.2-pro", 21, 168, true},
		{"OPENAI", "gpt-5.2", 1.75, 14, true},
		{"OPENAI", "gpt-5-pro", 15, 120, true},
		{"OPENAI", "gpt-5-mini", 0.25, 2, true},
		{"OPENAI", "gpt-5-nano", 0.05, 0.40, true},
		{"OPENAI", "gpt-5.1", 1.25, 10, true},
		{"OPENAI", "gpt-5", 1.25, 10, true},
		{"OPENAI", "gpt-4o-mini", 0.15, 0.60, true},
		{"OPENAI", "gpt-4o", 2.50, 10, true},
		{"OPENAI", "gpt-4-turbo", 10, 30, true},
		{"OPENAI", "gpt-4.1", 2, 8, true},
		{"OPENAI", "gpt-4", 30, 60, true},
		{"OPENAI", "gpt-3.5-turbo", 0.50, 1.50, true},
		{"OPENAI", "o3-mini", 1.10, 4.40, true},
		{"OPENAI", "o4-mini", 1.10, 4.40, true},
		{"OPENAI", "o3", 10, 40, true},
		{"OPENAI", "o1-mini", 3, 12, true},
		{"OPENAI", "o1", 15, 60, true},
		{"OPENAI", "text-davinci-003", 0, 0, false},
	})
}

func TestListPrice_GoogleFamily(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	assertPrices(t, []priceCase{
		{"GOOGLEAI", "gemini-3.8-flash", 0.75, 3.75, true},
		{"GOOGLEAI", "gemini-3.7-flash", 0.75, 3.75, true},
		{"GOOGLEAI", "gemini-3.6-flash", 0.75, 3.75, true},
		{"GOOGLEAI", "gemini-3.5-flash-lite", 0.30, 2.50, true},
		{"GOOGLEAI", "gemini-3.5-flash", 1.50, 9, true},
		{"GOOGLEAI", "gemini-3.1-pro", 2, 12, true},
		{"GOOGLEAI", "gemini-3.1-flash-lite", 0.25, 1.50, true},
		{"GOOGLEAI", "gemini-3-flash", 0.50, 3, true},
		{"GOOGLEAI", "gemini-3-pro-preview", 2, 12, true},
		{"GOOGLEAI", "gemini-2.5-pro", 1.25, 10, true},
		{"GOOGLEAI", "gemini-2.5-flash-lite", 0.10, 0.40, true},
		{"GOOGLEAI", "gemini-2.5-flash", 0.30, 2.50, true},
		{"GOOGLEAI", "gemini-2.0-flash", 0.075, 0.30, true},
		{"GOOGLEAI", "gemini-1.5-pro", 1.25, 5, true},
		{"GOOGLEAI", "gemini-1.5-flash", 0.075, 0.30, true},
		{"GOOGLEAI", "gemini-1.0-pro", 0, 0, false},
	})
}

func TestListPrice_GrokFamily(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	assertPrices(t, []priceCase{
		{"XAI", "grok-4.7", 2, 6, true},
		{"XAI", "grok-4.6", 2, 6, true},
		{"XAI", "grok-4.5", 2, 6, true},
		{"XAI", "grok-4.3", 1.25, 2.50, true},
		{"XAI", "grok-4.20", 1.25, 2.50, true},
		{"XAI", "grok-build-0.1", 1, 2, true},
		{"XAI", "grok-code-fast-1", 1, 2, true},
		{"XAI", "grok-3-mini", 1.25, 2.50, true},
		{"XAI", "grok-4-0709", 1.25, 2.50, true},
		{"XAI", "grok-2-1212", 2, 10, true},
		{"XAI", "grok-unknown", 5, 15, true},
	})
}

func TestListPrice_ZAIFamily(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	t.Setenv("ZAI_API_URL", "")
	t.Setenv("ZAI_USE_CODING_PLAN", "")
	assertPrices(t, []priceCase{
		{"ZAI", "glm-5.3-flashx", 0.37, 1.25, true},
		{"ZAI", "glm-5-3-flashx", 0.37, 1.25, true},
		{"ZAI", "glm-5.3-flash", 0.15, 0.50, true},
		{"ZAI", "glm-5.3", 1.40, 4.40, true},
		{"ZAI", "glm-5.2", 1.40, 4.40, true},
		{"ZAI", "glm-5-1", 1.40, 4.40, true},
		{"ZAI", "glm-5-turbo", 1.20, 4.00, true},
		{"ZAI", "glm-5v-turbo", 1.20, 4.00, true},
		{"ZAI", "glm-5", 1.00, 3.20, true},
		{"ZAI", "glm-4.7-flashx", 0.07, 0.40, true},
		{"ZAI", "glm-4.7-flash", 0, 0, true},
		{"ZAI", "glm-4.5-flash", 0, 0, true},
		{"ZAI", "glm-4.6v-flash", 0, 0, true},
		{"ZAI", "glm-4.6v-flashx", 0.04, 0.40, true},
		{"ZAI", "glm-4.6v", 0.30, 0.90, true},
		{"ZAI", "glm-4.5-airx", 1.10, 4.50, true},
		{"ZAI", "glm-4.5-air", 0.20, 1.10, true},
		{"ZAI", "glm-4.5-x", 2.20, 8.90, true},
		{"ZAI", "glm-4.5v", 0.60, 1.80, true},
		{"ZAI", "glm-4.7", 0.60, 2.20, true},
		{"ZAI", "glm-4.6", 0.60, 2.20, true},
		{"ZAI", "glm-4.5", 0.60, 2.20, true},
		// GLM-4 and older ids fall to the provider's conservative flat rate.
		{"ZAI", "glm-4-plus", 0.50, 0.50, true},
		{"ZAI", "", 0.50, 0.50, true},
	})
}

func TestListPrice_ZAICodingPlanIsKnownZero(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	t.Setenv("ZAI_API_URL", "")
	t.Setenv("ZAI_USE_CODING_PLAN", "true")
	assertPrices(t, []priceCase{{"ZAI", "glm-5.3", 0, 0, true}})
	write, read := CacheRates("ZAI", "glm-5.3")
	assert.Zero(t, write)
	assert.Zero(t, read)
}

func TestListPrice_DeepSeekAndNova(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	assertPrices(t, []priceCase{
		{"DEEPSEEK", "deepseek-v4-pro", 1.32, 3.96, true},
		{"DEEPSEEK", "deepseek-v4-flash", 0.44, 1.32, true},
		{"DEEPSEEK", "deepseek-r1", 0.55, 2.19, true},
		{"DEEPSEEK", "deepseek-reasoner", 0.55, 2.19, true},
		{"DEEPSEEK", "deepseek-chat", 0.27, 1.10, true},
		{"BEDROCK", "amazon.nova-micro-v1:0", 0.035, 0.14, true},
		{"BEDROCK", "amazon.nova-lite-v1:0", 0.06, 0.24, true},
		{"BEDROCK", "amazon.nova-pro-v1:0", 0.80, 3.20, true},
		{"BEDROCK", "amazon.nova-premier-v1:0", 2.50, 12.50, true},
		// Nova 2 is unpriced on purpose until the list price is verified.
		{"BEDROCK", "amazon.nova-2-lite-v1:0", 0, 0, false},
		{"BEDROCK", "amazon.nova-canvas-v1:0", 0, 0, false},
	})
}

func TestListPrice_ProviderFallbacks(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	assertPrices(t, []priceCase{
		{"MINIMAX", "minimax-m3", 0.30, 1.20, true},
		{"MINIMAX", "minimax-m2.5", 0.20, 1.10, true},
		{"MINIMAX", "", 0.20, 1.10, true},
		{"MOONSHOT", "kimi-k3", 3, 15, true},
		{"MOONSHOT", "kimi-k2.7-code-highspeed", 1.90, 8, true},
		{"MOONSHOT", "kimi-k2.7-code", 0.95, 4, true},
		{"MOONSHOT", "kimi-k2.6", 0.95, 4, true},
		{"MOONSHOT", "moonshot-v1-8k", 0.95, 4, true},
		{"COPILOT", "gpt-4o", 0, 0, true},
		{"COPILOT", "claude-sonnet-5", 0, 0, true},
		{"OLLAMA", "llama3", 0, 0, true},
		{"STACKSPOT", "stackspot-ai", 0, 0, true},
		{"UNKNOWN", "mystery-model", 0, 0, false},
	})
}

func TestListPrice_OpenRouterPassThrough(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	assertPrices(t, []priceCase{
		{"OPENROUTER", "anthropic/claude-sonnet-5", 2, 10, true},
		{"OPENROUTER", "openai/gpt-5.4", 2.50, 15, true},
		{"OPENROUTER", "google/gemini-2.5-pro", 1.25, 10, true},
		{"OPENROUTER", "deepseek/deepseek-v4-pro", 1.32, 3.96, true},
		{"OPENROUTER", "meta-llama/llama-3.3-70b", 0.20, 0.20, true},
		{"OPENROUTER", "mistralai/mistral-large", 0.20, 0.60, true},
		{"OPENROUTER", "qwen/qwen3-coder", 0.15, 0.15, true},
		// A family substring with no priced entry stays unpriced so /cost
		// lists it instead of reporting it as free.
		{"OPENROUTER", "anthropic/claude-mystery", 0, 0, false},
		{"OPENROUTER", "openai/gpt-mystery", 0, 0, false},
		{"OPENROUTER", "google/gemini-mystery", 0, 0, false},
		{"OPENROUTER", "vendor/unknown", 0, 0, false},
	})
	// OpenRouter reports the OpenAI-compatible subset schema whatever the
	// model, so a Claude slug is NOT additive there.
	assert.False(t, CacheTokensAdditive("OPENROUTER", "anthropic/claude-sonnet-5"))
}

func TestListPrice_DevinPrecedence(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	t.Cleanup(func() { ResetProvider(catalog.ProviderDevin) })
	ResetProvider(catalog.ProviderDevin)

	// Static table when the account listing never ran.
	assertPrices(t, []priceCase{
		{"DEVIN", "gpt-5.6-sol", 1.2, 6, true},
		{"DEVIN", "claude-opus-5-5-medium-fast", 8, 40, true},
		// An unlisted family is known-zero, never guessed from vendor tables.
		{"DEVIN", "claude-fable-5-1", 0, 0, true},
	})

	// The account's own listing outranks the static table.
	Register(catalog.ProviderDevin, "gpt-5.6-sol", Rate{InputPerMTok: 9, OutputPerMTok: 45})
	assertPrices(t, []priceCase{{"DEVIN", "gpt-5.6-sol", 9, 45, true}})

	// And the operator override outranks the listing.
	t.Setenv(OverrideEnv, "DEVIN:gpt-5.6-sol=1/2")
	assertPrices(t, []priceCase{{"DEVIN", "gpt-5.6-sol", 1, 2, true}})
}

func TestListPrice_OverrideWildcardAndAlias(t *testing.T) {
	t.Setenv(OverrideEnv, "OLLAMA:*=0.5/1.5;CLAUDEAI:claude-opus-5-5=7/70")
	assertPrices(t, []priceCase{
		{"OLLAMA", "llama3", 0.5, 1.5, true},
		{"OLLAMA", "anything", 0.5, 1.5, true},
		{"CLAUDEAI", "claude-opus-5-5", 7, 70, true},
		// Untouched providers keep their tables.
		{"OPENAI", "gpt-5.4", 2.50, 15, true},
	})
	// An alias the catalog resolves to the overridden id prices at the
	// override too.
	if meta, ok := catalog.Resolve("CLAUDEAI", "opus"); ok && meta.ID == "claude-opus-5-5" {
		assertPrices(t, []priceCase{{"CLAUDEAI", "opus", 7, 70, true}})
	}
}

func TestCacheRates_Families(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	type cacheCase struct {
		provider, model string
		write, read     float64
	}
	cases := []cacheCase{
		{"CLAUDEAI", "claude-fable-5-1", 12.5, 0.25},
		{"CLAUDEAI", "claude-opus-5-5", 5, 0.20},
		{"CLAUDEAI", "claude-sonnet-5", 2.5, 0.20},
		{"XAI", "grok-4.7", 0, 0.50},
		{"XAI", "grok-4.5", 0, 0.30},
		{"XAI", "grok-4.3", 0, 0.20},
		{"GOOGLEAI", "gemini-3.1-pro", 0, 0.20},
		{"OPENAI", "gpt-6-astra", 12.5, 1.0},
		{"OPENAI", "gpt-5.6-sol", 5, 0.40},
		{"OPENAI", "gpt-5.4", 0, 1.25},
		{"OPENAI", "o3", 0, 5},
		{"DEEPSEEK", "deepseek-v4-pro", 0, 0.33},
		{"MOONSHOT", "kimi-k2.6", 0, 0.1615},
		// No published cache rate: zero, so reads bill at the input price.
		{"MINIMAX", "minimax-m3", 0, 0},
		// Unpriced or known-zero models have no cache rate either.
		{"UNKNOWN", "mystery", 0, 0},
		{"OLLAMA", "llama3", 0, 0},
	}
	for _, tc := range cases {
		write, read := CacheRates(tc.provider, tc.model)
		assert.InDelta(t, tc.write, write, 1e-9, "%s/%s write", tc.provider, tc.model)
		assert.InDelta(t, tc.read, read, 1e-9, "%s/%s read", tc.provider, tc.model)
	}
}

func TestCacheWrite1hPerMTok(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	// Anthropic: 2x input for the 1-hour TTL.
	assert.InDelta(t, 4.0, CacheWrite1hPerMTok("CLAUDEAI", "claude-sonnet-5", 2.5), 1e-9)
	assert.InDelta(t, 20.0, CacheWrite1hPerMTok("CLAUDEAI", "claude-fable-5-1", 12.5), 1e-9)
	// Everyone else keeps the 5-minute write rate.
	assert.InDelta(t, 5.0, CacheWrite1hPerMTok("OPENAI", "gpt-5.6-sol", 5.0), 1e-9)
	// A claudeai provider with an unpriced model keeps the given rate.
	assert.InDelta(t, 1.5, CacheWrite1hPerMTok("CLAUDEAI", "mystery", 1.5), 1e-9)
}

func TestRatesFor_CarriesEveryKnob(t *testing.T) {
	t.Setenv(OverrideEnv, "")
	r := RatesFor("CLAUDEAI", "claude-sonnet-5")
	require.True(t, r.Known)
	assert.Equal(t, ModelRates{
		InputPerMTok: 2, OutputPerMTok: 10,
		CacheWritePerMTok: 2.5, CacheReadPerMTok: 0.2,
		Known: true, CacheTokensAdditive: true,
	}, r)

	g := RatesFor("GOOGLEAI", "gemini-3.1-pro")
	assert.True(t, g.ReasoningTokensAdditive)
	assert.False(t, g.CacheTokensAdditive)

	u := RatesFor("UNKNOWN", "mystery")
	assert.False(t, u.Known)
	assert.Zero(t, u.InputPerMTok)
}
