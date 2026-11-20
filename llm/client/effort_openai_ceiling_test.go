/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import "testing"

// The level sent to OpenAI is the requested one lowered to what the model
// accepts, per the API's own answers (Nov 19 2026).
func TestOpenAIModelReasoningEffort_Ceiling(t *testing.T) {
	for _, tc := range []struct {
		model       string
		xhigh, max_ string
	}{
		// GPT-6 family and GPT-5.6: xhigh and max are accepted.
		{"gpt-6.2-sol", "xhigh", "max"},
		{"gpt-6.2-astra", "xhigh", "max"},
		{"gpt-6.1-sol", "xhigh", "max"},
		{"gpt-6.1-terra", "xhigh", "max"},
		{"gpt-6-astra", "xhigh", "max"},
		{"gpt-6-sol", "xhigh", "max"},
		{"gpt-6-luna", "xhigh", "max"},
		{"GPT-6.2-Sol", "xhigh", "max"},
		{"gpt-5.6-sol", "xhigh", "max"},
		{"gpt-5.6-luna", "xhigh", "max"},
		// GPT-5.2 .. 5.5: xhigh is the top, max is refused.
		{"gpt-5.5", "xhigh", "xhigh"},
		{"gpt-5.5-pro", "xhigh", "xhigh"},
		{"gpt-5.4", "xhigh", "xhigh"},
		{"gpt-5.4-mini", "xhigh", "xhigh"},
		{"gpt-5.3-codex", "xhigh", "xhigh"},
		{"gpt-5.2", "xhigh", "xhigh"},
		// GPT-5, 5.1 and the o-series stop at high.
		{"gpt-5", "high", "high"},
		{"gpt-5.1", "high", "high"},
		{"gpt-5-mini", "high", "high"},
		{"o3", "high", "high"},
		{"gpt-oss-120b", "high", "high"},
		{"custom-reasoning", "high", "high"},
	} {
		if got := OpenAIModelReasoningEffort(tc.model, EffortXHigh); got != tc.xhigh {
			t.Errorf("%s xhigh = %q, want %q", tc.model, got, tc.xhigh)
		}
		if got := OpenAIModelReasoningEffort(tc.model, EffortMax); got != tc.max_ {
			t.Errorf("%s max = %q, want %q", tc.model, got, tc.max_)
		}
		// The levels every reasoning model takes are unchanged.
		for e, want := range map[SkillEffort]string{EffortUnset: "", EffortLow: "low", EffortMedium: "medium", EffortHigh: "high"} {
			if got := OpenAIModelReasoningEffort(tc.model, e); got != want {
				t.Errorf("%s %q = %q, want %q", tc.model, e, got, want)
			}
		}
	}
	// A model that does not reason carries no field at any level.
	for _, e := range []SkillEffort{EffortLow, EffortHigh, EffortXHigh, EffortMax} {
		if got := OpenAIModelReasoningEffort("gpt-4.1", e); got != "" {
			t.Errorf("gpt-4.1 %q = %q, want nothing", e, got)
		}
	}
	// The shared mapping other surfaces use still stops at high.
	if got := OpenAIReasoningEffort("gpt-6.2-sol", EffortMax); got != "high" {
		t.Errorf("shared mapping = %q, want high", got)
	}
}
