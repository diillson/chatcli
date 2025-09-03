package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolve(t *testing.T) {
	testCases := []struct {
		name       string
		provider   string
		model      string
		expectedID string
		shouldFind bool
	}{
		{"Exact Match OpenAI", ProviderOpenAI, "gpt-4o-mini", "gpt-4o-mini", true},
		{"Alias Match ClaudeAI", ProviderClaudeAI, "claude-3-5-sonnet", "claude-3-5-sonnet-20241022", true},
		{"Prefix Match ClaudeAI", ProviderClaudeAI, "claude-3-5-sonnet-20241022-preview", "claude-3-5-sonnet-20241022", true},
		{"Case Insensitive", ProviderOpenAI, "GPT-4O", "gpt-4o", true},
		{"Gemini Flash Lite", ProviderGoogleAI, "gemini-2.0-flash-lite", "gemini-2.0-flash-lite", true},
		{"GPT-5 Alias", ProviderOpenAI, "gpt-5-mini", "gpt-5", true},
		{"Not Found", ProviderOpenAI, "gpt-nonexistent", "", false},
		{"Wrong Provider", ProviderStackSpot, "gpt-4o", "", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			meta, found := Resolve(tc.provider, tc.model)
			assert.Equal(t, tc.shouldFind, found)
			if tc.shouldFind {
				assert.Equal(t, tc.expectedID, meta.ID)
			}
		})
	}
}

func TestGetMaxTokens(t *testing.T) {
	// Caso 1: Override tem precedência
	tokens := GetMaxTokens(ProviderOpenAI, "gpt-4o", 12345)
	assert.Equal(t, 12345, tokens, "Override should have the highest priority")

	// Caso 2: Valor do catálogo
	tokens = GetMaxTokens(ProviderClaudeAI, "claude-3-haiku", 0)
	assert.Equal(t, 42000, tokens, "Should get value from catalog for claude-3-haiku")

	// Caso 3: Fallback para modelo desconhecido
	tokens = GetMaxTokens(ProviderOpenAI, "unknown-model", 0)
	assert.Equal(t, 40000, tokens, "Should use fallback value for unknown OpenAI model")

	// Caso 4: Fallback para provider desconhecido
	tokens = GetMaxTokens("UNKNOWN_PROVIDER", "some-model", 0)
	assert.Equal(t, 50000, tokens, "Should use the default fallback value")
}

func TestGetPreferredAPI(t *testing.T) {
	assert.Equal(t, APIResponses, GetPreferredAPI(ProviderOpenAI, "gpt-5"))
	assert.Equal(t, APIChatCompletions, GetPreferredAPI(ProviderOpenAI, "gpt-4o"))
	assert.Equal(t, APIAnthropicMessages, GetPreferredAPI(ProviderClaudeAI, "claude-3-opus"))
	assert.Equal(t, APIAssistants, GetPreferredAPI(ProviderOpenAIAssistant, "gpt-4o")) // Provider específico
	assert.Equal(t, PreferredAPI("gemini_api"), GetPreferredAPI(ProviderGoogleAI, "gemini-2.5-pro"))
}
