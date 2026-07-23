/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/catalog"
)

// Boot regression for the class of bug where a provider missing from the
// boot model table left cli.Model empty: the internal client talked to the
// right model while catalog sizing resolved (provider, "") to conservative
// fallbacks, so the whole session ran with a degraded max-tokens and
// context window. /model worked; starting preconfigured did not.
func TestConfigureProviderAndModelCoversEveryProvider(t *testing.T) {
	for provider, src := range bootModelSources {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("LLM_PROVIDER", provider)
			t.Setenv(src.envVar, "custom-model-under-test")
			cli := &ChatCLI{}
			cli.configureProviderAndModel()
			if cli.Provider != provider {
				t.Fatalf("Provider = %q, want %q", cli.Provider, provider)
			}
			if cli.Model != "custom-model-under-test" {
				t.Fatalf("Model = %q — the %s env must reach cli.Model at boot", cli.Model, src.envVar)
			}
		})
	}
}

// The exact scenario reported in production: LLM_PROVIDER=bedrock
// (lowercase!) plus BEDROCK_MODEL set. Before the fix the lowercase value
// failed every exact comparison AND Bedrock had no entry in the boot
// chain, so a 128K model sized as the tiny provider fallback.
func TestConfigureProviderAndModelBedrockFromEnv(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "bedrock")
	t.Setenv("BEDROCK_MODEL", "global.anthropic.claude-opus-4-8")
	cli := &ChatCLI{}
	cli.configureProviderAndModel()

	if cli.Provider != "BEDROCK" {
		t.Fatalf("Provider = %q, lowercase env must normalize to BEDROCK", cli.Provider)
	}
	if got := catalog.GetMaxTokens(cli.Provider, cli.Model, 0); got != 128000 {
		t.Fatalf("boot-configured Opus 4.8 must size at 128000 max tokens, got %d", got)
	}
	if got := catalog.GetContextWindow(cli.Provider, cli.Model); got != 1000000 {
		t.Fatalf("boot-configured Opus 4.8 must size at the 1M window, got %d", got)
	}
}

func TestConfigureProviderAndModelDefaultsWhenEnvUnset(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "BEDROCK")
	t.Setenv("BEDROCK_MODEL", "")
	cli := &ChatCLI{}
	cli.configureProviderAndModel()
	if cli.Model != config.DefaultBedrockModel {
		t.Fatalf("Model = %q, want the catalog default %q", cli.Model, config.DefaultBedrockModel)
	}
}

func TestConfigureProviderAndModelAssistantFallbackChain(t *testing.T) {
	t.Setenv("LLM_PROVIDER", "OPENAI_ASSISTANT")
	t.Setenv("OPENAI_ASSISTANT_MODEL", "")
	t.Setenv("OPENAI_MODEL", "gpt-5.2")
	cli := &ChatCLI{}
	cli.configureProviderAndModel()
	if cli.Model != "gpt-5.2" {
		t.Fatalf("Model = %q, assistant must fall back to OPENAI_MODEL", cli.Model)
	}
}
