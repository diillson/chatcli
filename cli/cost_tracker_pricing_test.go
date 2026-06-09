package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetModelPricing pins the per-family pricing returned by the dispatcher.
// Each case is the same shape (provider, model, expected input, expected output)
// so adding a new family/model is a one-line addition.
func TestGetModelPricing(t *testing.T) {
	cases := []struct {
		name            string
		provider, model string
		wantIn, wantOut float64
	}{
		// Anthropic
		{"claude fable 5", "CLAUDEAI", "claude-fable-5", 10.0, 50.0},
		{"claude opus 4.8", "CLAUDEAI", "claude-opus-4-8", 5.0, 25.0},
		{"claude opus 4.7", "CLAUDEAI", "claude-opus-4-7", 5.0, 25.0},
		{"claude opus 4.6 bedrock id", "BEDROCK", "global.anthropic.claude-opus-4-6-20260115-v1:0", 5.0, 25.0},
		{"claude opus 4.5", "CLAUDEAI", "claude-opus-4-5", 5.0, 25.0},
		{"claude opus legacy", "CLAUDEAI", "claude-3-opus", 15.0, 75.0},
		{"claude opus 4.1 legacy", "CLAUDEAI", "claude-opus-4-1", 15.0, 75.0},
		{"claude sonnet", "CLAUDEAI", "claude-sonnet-4-6", 3.0, 15.0},
		{"claude haiku 4.5", "CLAUDEAI", "claude-haiku-4-5", 1.0, 5.0},
		{"claude haiku legacy", "CLAUDEAI", "claude-3-haiku", 0.25, 1.25},

		// OpenAI — specific before generic.
		{"gpt-4o-mini before gpt-4o", "OPENAI", "gpt-4o-mini", 0.15, 0.60},
		{"gpt-4o", "OPENAI", "gpt-4o", 2.50, 10.0},
		{"gpt-4-turbo", "OPENAI", "gpt-4-turbo", 10.0, 30.0},
		{"gpt-4.1", "OPENAI", "gpt-4.1", 2.0, 8.0},
		{"gpt-4 bare", "OPENAI", "gpt-4", 30.0, 60.0},
		{"gpt-3.5", "OPENAI", "gpt-3.5-turbo", 0.50, 1.50},
		{"o3-mini", "OPENAI", "o3-mini", 1.10, 4.40},
		{"o4-mini", "OPENAI", "o4-mini", 1.10, 4.40},
		{"o3", "OPENAI", "o3", 10.0, 40.0},
		{"o1-mini before o1", "OPENAI", "o1-mini", 3.0, 12.0},
		{"o1", "OPENAI", "o1", 15.0, 60.0},

		// Google
		{"gemini 2.5 pro", "GOOGLEAI", "gemini-2.5-pro", 1.25, 10.0},
		{"gemini 2.5 flash", "GOOGLEAI", "gemini-2.5-flash", 0.15, 0.60},
		{"gemini 2.0", "GOOGLEAI", "gemini-2.0-flash", 0.075, 0.30},
		{"gemini 1.5 pro", "GOOGLEAI", "gemini-1.5-pro", 1.25, 5.0},
		{"gemini 1.5 flash", "GOOGLEAI", "gemini-1.5-flash", 0.075, 0.30},

		// xAI Grok
		{"grok-3", "XAI", "grok-3", 3.0, 15.0},
		{"grok-2", "XAI", "grok-2", 2.0, 10.0},
		{"grok generic", "XAI", "grok-4", 5.0, 15.0},

		// DeepSeek
		{"deepseek-r1 before deepseek", "OPENROUTER", "deepseek-r1", 0.55, 2.19},
		{"deepseek bare", "OPENROUTER", "deepseek-chat", 0.27, 1.10},

		// Provider-keyed fallbacks
		{"minimax via model", "OTHER", "minimax-m2.7", 0.20, 1.10},
		{"minimax via provider", "MINIMAX", "anything", 0.20, 1.10},
		{"zai via provider", "ZAI", "anything", 0.50, 0.50},
		{"zai via glm model", "OTHER", "glm-5", 0.50, 0.50},

		// Moonshot (Kimi) — K2.6 list price, conservative single tier.
		{"moonshot via provider", "MOONSHOT", "kimi-k2.6", 0.95, 4.00},
		{"kimi-k2.5", "MOONSHOT", "kimi-k2.5", 0.95, 4.00},
		{"moonshot-v1", "MOONSHOT", "moonshot-v1-128k", 0.95, 4.00},

		{"copilot", "COPILOT", "gpt-4o", 2.50, 10.0},
		{"ollama zero", "OLLAMA", "llama3", 0.0, 0.0},
		{"stackspot zero", "STACKSPOT", "stackspotai", 0.0, 0.0},

		// Unknown provider+model defaults to zero.
		{"unknown", "WHATEVER", "no-match", 0.0, 0.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out := getModelPricing(tc.provider, tc.model)
			assert.Equal(t, tc.wantIn, in, "input cost")
			assert.Equal(t, tc.wantOut, out, "output cost")
		})
	}
}

// TestGetModelPricing_CaseInsensitive ensures the dispatcher lowercases both
// inputs so the caller can pass capitalized provider/model names without
// silently falling through to the zero default.
func TestGetModelPricing_CaseInsensitive(t *testing.T) {
	in, out := getModelPricing("MOONSHOT", "Kimi-K2.6")
	assert.Equal(t, 0.95, in)
	assert.Equal(t, 4.0, out)

	in, out = getModelPricing("CLAUDEAI", "Claude-3-Opus")
	assert.Equal(t, 15.0, in)
	assert.Equal(t, 75.0, out)
}
