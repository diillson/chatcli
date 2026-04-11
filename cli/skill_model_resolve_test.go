package cli

import (
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

func TestFamilyProviderOf(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Claude family
		{"claude-sonnet-4-6", "CLAUDEAI"},
		{"claude-opus-4-5-20251001", "CLAUDEAI"},
		{"sonnet-4", "CLAUDEAI"},
		{"haiku-4", "CLAUDEAI"},
		{"opus", "CLAUDEAI"},

		// OpenAI family
		{"gpt-5", "OPENAI"},
		{"gpt-4o", "OPENAI"},
		{"o1-preview", "OPENAI"},
		{"o3-mini", "OPENAI"},
		{"chatgpt-4o-latest", "OPENAI"},

		// Google
		{"gemini-2.5-pro", "GOOGLEAI"},
		{"gemini-3-pro", "GOOGLEAI"},

		// xAI
		{"grok-2", "XAI"},

		// ZAI
		{"glm-4-flash", "ZAI"},

		// MiniMax
		{"minimax-m1", "MINIMAX"},

		// Open weights
		{"llama-3.1-70b", "OLLAMA"},
		{"mistral-large", "OLLAMA"},
		{"qwen-2.5-coder", "OLLAMA"},
		{"deepseek-v3", "OLLAMA"},

		// Unknowns
		{"random-custom-model", ""},
		{"", ""},
		{"zz-9-plural-z-alpha", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := client.FamilyProviderOf(tc.in)
			if got != tc.want {
				t.Errorf("FamilyProviderOf(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCatalogProviderOf(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"gpt-5", "OPENAI"},
		{"claude-sonnet-4-6", "CLAUDEAI"},
		{"claude-opus-4-5", "CLAUDEAI"},
		{"gemini-2.5-pro", "GOOGLEAI"},
		{"this-model-does-not-exist-anywhere", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := client.CatalogProviderOf(tc.in)
			if got != tc.want {
				t.Errorf("CatalogProviderOf(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestContainsProvider(t *testing.T) {
	list := []string{"CLAUDEAI", "OPENAI", "GOOGLEAI"}
	if !client.ContainsProvider(list, "claudeai") {
		t.Error("case-insensitive lookup should match")
	}
	if !client.ContainsProvider(list, "OPENAI") {
		t.Error("exact match should match")
	}
	if client.ContainsProvider(list, "XAI") {
		t.Error("absent provider should not match")
	}
	if client.ContainsProvider(nil, "OPENAI") {
		t.Error("nil list should not match")
	}
}
