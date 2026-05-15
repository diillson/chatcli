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

	cases := []struct {
		name     string
		provider string
		wantIn   float64
		wantOut  float64
	}{
		{"claude upper", "CLAUDEAI", 3.0, 15.0},
		{"claude lower", "claudeai", 3.0, 15.0},
		{"openai", "OPENAI", 10.0, 30.0},
		{"googleai", "GOOGLEAI", 1.25, 5.0},
		{"xai", "XAI", 3.0, 15.0},
		{"zai", "ZAI", 1.0, 4.0},
		{"minimax", "MINIMAX", 0.3, 1.2},
		// Moonshot was added with the provider; the Floor 3 per-path
		// threshold caught it being untested on the first PR run.
		{"moonshot upper", "MOONSHOT", 0.95, 4.0},
		{"moonshot lower", "moonshot", 0.95, 4.0},
		{"copilot", "COPILOT", 10.0, 30.0},
		{"openrouter", "OPENROUTER", 2.0, 8.0},
		// Unknown provider falls into the conservative default.
		{"default fallback", "UNKNOWN", 1.0, 3.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ct.getTokenPricing(ctx, "default", tc.provider)
			if got.InputPerMillion != tc.wantIn {
				t.Errorf("input = %v, want %v", got.InputPerMillion, tc.wantIn)
			}
			if got.OutputPerMillion != tc.wantOut {
				t.Errorf("output = %v, want %v", got.OutputPerMillion, tc.wantOut)
			}
		})
	}
}
