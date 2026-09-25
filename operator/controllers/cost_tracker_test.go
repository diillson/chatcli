/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestCostTracker_GetTokenPricing pins the default per-provider pricing
// dispatcher so a new provider can never land in cost_tracker.go without
// a test covering its branch. The Quality Gate's Floor 3 per-path target
// for operator/controllers/** is 70 percent; an untested case immediately
// breaches it.
func TestCostTracker_GetTokenPricing(t *testing.T) {
	ct := &CostTracker{client: fake.NewClientBuilder().Build()}
	ctx := context.Background()

	t.Setenv("CHATCLI_MODEL_PRICING", "")

	cases := []struct {
		name     string
		provider string
		model    string
		wantIn   float64
		wantOut  float64
	}{
		// A model the shared engine prices resolves at its list price,
		// exactly as the CLI's /cost would — the ledger and the session
		// never disagree about the same call.
		{"claude opus 5.5", "CLAUDEAI", "claude-opus-5-5", 4.0, 20.0},
		{"claude sonnet 5 lower", "claudeai", "claude-sonnet-5", 2.0, 10.0},
		{"openai gpt-5.6-sol", "OPENAI", "gpt-5.6-sol", 4.0, 20.0},
		{"gemini 3.1 pro", "GOOGLEAI", "gemini-3.1-pro", 2.0, 12.0},
		{"grok 4.7", "XAI", "grok-4.7", 2.0, 6.0},
		{"bedrock nova pro", "BEDROCK", "amazon.nova-pro-v1:0", 0.80, 3.20},
		{"openrouter claude", "OPENROUTER", "anthropic/claude-opus-5.5", 4.0, 20.0},
		// Providers the engine prices by name even without a model id.
		{"zai", "ZAI", "", 0.50, 0.50},
		{"minimax", "MINIMAX", "", 0.20, 1.10},
		// Moonshot was added with the provider; the Floor 3 per-path
		// threshold caught it being untested on the first PR run.
		{"moonshot upper", "MOONSHOT", "", 0.95, 4.0},
		{"moonshot lower", "moonshot", "", 0.95, 4.0},
		// Subscriptions are known-zero: Copilot debits premium requests,
		// the Devin CLI bills the Cognition account, not a per-token
		// invoice.
		{"copilot", "COPILOT", "gpt-4o", 0.0, 0.0},
		{"devin upper", "DEVIN", "", 0.0, 0.0},
		{"devin lower", "devin", "", 0.0, 0.0},
		// A model the engine does not know falls back to the legacy
		// per-provider defaults instead of pricing at zero.
		{"claude unknown model", "CLAUDEAI", "", 3.0, 15.0},
		{"openai unknown model", "OPENAI", "", 10.0, 30.0},
		{"googleai unknown model", "GOOGLEAI", "", 1.25, 5.0},
		{"xai unknown model", "XAI", "", 3.0, 15.0},
		{"openrouter unknown model", "OPENROUTER", "", 2.0, 8.0},
		{"default fallback", "UNKNOWN", "", 1.0, 3.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ct.getTokenPricing(ctx, "default", tc.provider, tc.model)
			if got.InputPerMillion != tc.wantIn {
				t.Errorf("input = %v, want %v", got.InputPerMillion, tc.wantIn)
			}
			if got.OutputPerMillion != tc.wantOut {
				t.Errorf("output = %v, want %v", got.OutputPerMillion, tc.wantOut)
			}
		})
	}
}
