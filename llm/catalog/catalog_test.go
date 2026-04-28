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
		{"Alias Match ClaudeAI", ProviderClaudeAI, "claude-3-5-sonnet", "claude-sonnet-3-5-20241022", true},
		{"Prefix Match ClaudeAI", ProviderClaudeAI, "claude-3-5-sonnet-20241022-preview", "claude-sonnet-3-5-20241022", true},
		{"Case Insensitive", ProviderOpenAI, "GPT-4O", "gpt-4o", true},
		{"Gemini Flash Lite", ProviderGoogleAI, "gemini-2.0-flash-lite", "gemini-2.0-flash-lite", true},
		{"GPT-5 Alias", ProviderOpenAI, "gpt-5-mini", "gpt-5", true},
		{"Not Found", ProviderOpenAI, "gpt-nonexistent", "", false},
		{"Wrong Provider", ProviderStackSpot, "gpt-4o", "", false},
		// Regression: the bare "opus-4" alias on the 4.0 entry is a prefix
		// of all opus-4-X shortcuts. Newer entries MUST be declared first
		// in the registry so their exact-alias match wins over 4.0's
		// loose prefix match. Each of these silently resolved to the 4.0
		// entry (ctx=20K) before the fix.
		{"Claude Opus 4.5 shortcut", ProviderClaudeAI, "opus-4-5", "claude-opus-4-5", true},
		{"Claude Opus 4.6 shortcut", ProviderClaudeAI, "opus-4-6", "claude-opus-4-6", true},
		{"Claude Opus 4.7 shortcut", ProviderClaudeAI, "opus-4-7", "claude-opus-4-7", true},
		{"Claude Opus 4.7 full ID", ProviderClaudeAI, "claude-opus-4-7", "claude-opus-4-7", true},
		{"Claude Sonnet 4.7 shortcut", ProviderClaudeAI, "sonnet-4-7", "claude-sonnet-4-7", true},
		// Backward compat: bare "opus-4" still resolves to the 4.0 entry
		{"Claude Opus 4 bare alias", ProviderClaudeAI, "opus-4", "claude-opus-4-20250514", true},
		// gpt-5.5 family — released Apr 23 2026. Pin both the base and
		// the pro variant; the registry order also matters here so 5.5
		// is not shadowed by an earlier 5.x prefix match.
		{"GPT-5.5 exact", ProviderOpenAI, "gpt-5.5", "gpt-5.5", true},
		{"GPT-5.5 Pro exact", ProviderOpenAI, "gpt-5.5-pro", "gpt-5.5-pro", true},
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

	// Caso 2: Valor do catálogo. Haiku 3 expõe 4096 tokens de output
	// (limite real publicado pela Anthropic, não o número conservador
	// inflado de 42K que o catálogo carregava antes da auditoria).
	tokens = GetMaxTokens(ProviderClaudeAI, "claude-3-haiku", 0)
	assert.Equal(t, 4096, tokens, "Should get value from catalog for claude-3-haiku")

	// Caso 3: Fallback para modelo desconhecido. Após a auditoria de
	// catálogo (Abr 2026) os fallbacks foram alinhados com os limites
	// oficiais — 16384 é o piso "OpenAI gpt-* genérico" para modelos
	// fora do registry, coerente com o cap real de gpt-4o (16K).
	tokens = GetMaxTokens(ProviderOpenAI, "unknown-model", 0)
	assert.Equal(t, 16384, tokens, "Should use fallback value for unknown OpenAI model")

	// Caso 4: Fallback para provider desconhecido
	tokens = GetMaxTokens("UNKNOWN_PROVIDER", "some-model", 0)
	assert.Equal(t, 50000, tokens, "Should use the default fallback value")
}

func TestGetPreferredAPI(t *testing.T) {
	assert.Equal(t, APIResponses, GetPreferredAPI(ProviderOpenAI, "gpt-5"))
	assert.Equal(t, APIResponses, GetPreferredAPI(ProviderOpenAI, "gpt-5.5"))
	assert.Equal(t, APIResponses, GetPreferredAPI(ProviderOpenAI, "gpt-5.5-pro"))
	assert.Equal(t, APIChatCompletions, GetPreferredAPI(ProviderOpenAI, "gpt-4o"))
	assert.Equal(t, APIAnthropicMessages, GetPreferredAPI(ProviderClaudeAI, "claude-3-opus"))
	assert.Equal(t, APIAssistants, GetPreferredAPI(ProviderOpenAIAssistant, "gpt-4o")) // Provider específico
	assert.Equal(t, PreferredAPI("gemini_api"), GetPreferredAPI(ProviderGoogleAI, "gemini-2.5-pro"))
}

// TestGPT55LimitsAndCapabilities pins the published Apr 23 2026 specs:
// 1,050,000-token context window and 128,000 max output for both the
// base and pro variants. If OpenAI revises these limits, the failure
// here is the signal to update the registry entries (and the doc note
// next to them) rather than silently drifting.
func TestGPT55LimitsAndCapabilities(t *testing.T) {
	for _, id := range []string{"gpt-5.5", "gpt-5.5-pro"} {
		meta, ok := Resolve(ProviderOpenAI, id)
		assert.True(t, ok, "expected %s to resolve", id)
		assert.Equal(t, 1050000, meta.ContextWindow, "%s context window", id)
		assert.Equal(t, 128000, meta.MaxOutputTokens, "%s max output", id)
		assert.Equal(t, APIResponses, meta.PreferredAPI, "%s preferred API", id)
		assert.Contains(t, meta.Capabilities, "tools", "%s should advertise tools", id)
		assert.Contains(t, meta.Capabilities, "json_mode", "%s should advertise json_mode", id)
		assert.Contains(t, meta.Capabilities, "vision", "%s should advertise vision", id)
	}

	// Catalog max-tokens lookup honors the explicit entry value, not the
	// generic gpt-5 fallback (50000) defined in GetMaxTokens.
	assert.Equal(t, 128000, GetMaxTokens(ProviderOpenAI, "gpt-5.5", 0))
	assert.Equal(t, 128000, GetMaxTokens(ProviderOpenAI, "gpt-5.5-pro", 0))
}
