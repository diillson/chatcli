/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package catalog

import (
	"os"
	"strconv"
	"strings"
)

// maxTokensEnvByProvider maps each provider (canonical, upper-cased name) to
// the operator-facing env var that forces the per-request max_tokens for
// it. Shared by every surface that resolves max tokens without a session
// override: the interactive CLI, the gRPC server and the RPC backends read
// the same table, so an operator's *_MAX_TOKENS setting means the same
// thing everywhere.
var maxTokensEnvByProvider = map[string]string{
	ProviderOpenAI:    "OPENAI_MAX_TOKENS",
	ProviderClaudeAI:  "ANTHROPIC_MAX_TOKENS",
	ProviderGoogleAI:  "GOOGLEAI_MAX_TOKENS",
	ProviderXAI:       "XAI_MAX_TOKENS",
	ProviderZAI:       "ZAI_MAX_TOKENS",
	ProviderMiniMax:   "MINIMAX_MAX_TOKENS",
	ProviderMoonshot:  "MOONSHOT_MAX_TOKENS",
	ProviderOllama:    "OLLAMA_MAX_TOKENS",
	ProviderStackSpot: "STACKSPOT_MAX_TOKENS",
	ProviderCopilot:   "COPILOT_MAX_TOKENS",
	// BEDROCK_MAX_TOKENS is the primary env the Bedrock client reads (it
	// also accepts ANTHROPIC_MAX_TOKENS as a secondary, handled client-side).
	ProviderBedrock:    "BEDROCK_MAX_TOKENS",
	ProviderOpenRouter: "OPENROUTER_MAX_TOKENS",
}

// MaxTokensEnvVar returns the env var that overrides max_tokens for the
// provider, and false when the provider has none registered.
func MaxTokensEnvVar(provider string) (string, bool) {
	name, ok := maxTokensEnvByProvider[strings.ToUpper(strings.TrimSpace(provider))]
	return name, ok
}

// MaxTokensEnvOverride reads the provider's override env var and returns
// its positive-integer value, or 0 when the env is unset, empty,
// non-numeric or non-positive. Unknown providers yield 0 so callers fall
// back to the catalog default.
func MaxTokensEnvOverride(provider string) int {
	envName, ok := MaxTokensEnvVar(provider)
	if !ok {
		return 0
	}
	raw := strings.TrimSpace(os.Getenv(envName))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// EffectiveMaxTokens resolves the max_tokens for one request: an explicit
// positive request value wins, then the provider's env override, then the
// catalog ceiling for the model.
func EffectiveMaxTokens(provider, model string, requested int) int {
	if requested > 0 {
		return requested
	}
	return GetMaxTokens(provider, model, MaxTokensEnvOverride(provider))
}
