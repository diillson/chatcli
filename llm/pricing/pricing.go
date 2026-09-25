/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package pricing is ChatCLI's USD pricing engine, a leaf every surface
// prices token usage with — the CLI's cost tracker and /cost, the gRPC
// server and the Kubernetes operator — so they can never disagree about
// the price of the same call.
//
// It holds three things. The static tables of published list prices per
// provider and model family, with each provider's cache discount and
// long-context tier (tables.go, devin.go). The rates discovered at
// runtime: the per-account prices a provider reports in its own model
// listing (the Devin CLI's cost_summary; Register/Lookup) and the
// operator's CHATCLI_MODEL_PRICING override (override.go), which outranks
// everything. And the cost formula itself (engine.go): RatesFor resolves
// every knob for a provider+model, RecordCost prices a cumulative token
// ledger, and CostOf prices one provider-reported usage payload in a
// single call.
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
