/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package pricing is a leaf registry of per-model token rates discovered at
// runtime — the per-account prices a provider reports in its own model
// listing (the Devin CLI's cost_summary today). The cost tracker's static
// tables stay the source for providers with published list prices; this
// registry covers the ones whose rate is only knowable from the account.
package pricing

import (
	"strings"
	"sync"
)

// Rate is a model's price in USD per million tokens.
type Rate struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

var (
	mu    sync.RWMutex
	rates = map[string]Rate{}
)

func key(provider, model string) string {
	return strings.ToUpper(strings.TrimSpace(provider)) + "|" + strings.ToLower(strings.TrimSpace(model))
}

// Register stores the rate for provider+model (case-insensitive), replacing
// any previous value. A blank model or a rate with no positive component is
// ignored: zero is never "known free" here, it is "unlisted".
func Register(provider, model string, r Rate) {
	if strings.TrimSpace(model) == "" || (r.InputPerMTok <= 0 && r.OutputPerMTok <= 0) {
		return
	}
	mu.Lock()
	rates[key(provider, model)] = r
	mu.Unlock()
}

// Lookup returns the registered rate for provider+model.
func Lookup(provider, model string) (Rate, bool) {
	mu.RLock()
	defer mu.RUnlock()
	r, ok := rates[key(provider, model)]
	return r, ok
}

// ResetProvider drops every rate registered for provider.
func ResetProvider(provider string) {
	prefix := strings.ToUpper(strings.TrimSpace(provider)) + "|"
	mu.Lock()
	for k := range rates {
		if strings.HasPrefix(k, prefix) {
			delete(rates, k)
		}
	}
	mu.Unlock()
}
