/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import "testing"

func TestForLLMProvider(t *testing.T) {
	cases := map[string]struct {
		want ProviderID
		ok   bool
	}{
		"CLAUDEAI":  {ProviderAnthropic, true},
		"claudeai":  {ProviderAnthropic, true},
		"OPENAI":    {ProviderOpenAI, true},
		"COPILOT":   {ProviderGitHubCopilot, true},
		"BEDROCK":   {"", false},
		"STACKSPOT": {"", false},
		"NOPE":      {"", false},
	}
	for in, tc := range cases {
		got, ok := ForLLMProvider(in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ForLLMProvider(%q) = (%q, %v), want (%q, %v)", in, got, ok, tc.want, tc.ok)
		}
	}
}
