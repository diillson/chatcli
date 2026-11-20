/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openrouter

import (
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

// OpenAI slugs follow the model's own ceiling; every other upstream keeps
// the three levels OpenRouter documents for all of them.
func TestOpenRouterEffort(t *testing.T) {
	for _, tc := range []struct {
		model string
		e     client.SkillEffort
		want  string
	}{
		{"openai/gpt-6.2-sol", client.EffortMax, "max"},
		{"openai/gpt-6.2-astra", client.EffortXHigh, "xhigh"},
		{"OpenAI/GPT-6.1-Sol", client.EffortMax, "max"},
		{"openai/gpt-5.5", client.EffortMax, "xhigh"},
		{"openai/gpt-5", client.EffortMax, "high"},
		{"openai/gpt-6.2-sol", client.EffortMedium, "medium"},
		{"openai/gpt-6.2-sol", client.EffortUnset, ""},
		// A non-reasoning OpenAI slug keeps what it was sent before.
		{"openai/gpt-4o", client.EffortMax, "high"},
		{"anthropic/claude-fable-5.2", client.EffortMax, "high"},
		{"google/gemini-4-neon", client.EffortXHigh, "high"},
		{"google/gemini-4-neon", client.EffortLow, "low"},
		{"moonshotai/kimi-k3.1-pro", client.EffortUnset, ""},
	} {
		if got := openRouterEffort(tc.model, tc.e); got != tc.want {
			t.Errorf("openRouterEffort(%q, %q) = %q, want %q", tc.model, tc.e, got, tc.want)
		}
	}
}
