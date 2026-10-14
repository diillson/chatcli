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

// MaxTokensEnvVar returns the env var that overrides max_tokens for the
// provider, and false when the provider has none registered. The env lives
// on the provider's descriptor, so every surface that resolves max tokens
// without a session override (the interactive CLI, the gRPC server and the
// RPC backends) reads the same value and an operator's *_MAX_TOKENS setting
// means the same thing everywhere.
func MaxTokensEnvVar(provider string) (string, bool) {
	d, ok := LookupProvider(provider)
	if !ok || d.MaxOutputEnv == "" {
		return "", false
	}
	return d.MaxOutputEnv, true
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
