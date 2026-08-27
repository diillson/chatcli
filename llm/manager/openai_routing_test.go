package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOpenAIPreferResponses fixa a decisão Responses vs chat-completions do
// provider OPENAI. O caso de regressão é o gateway custom: a preferência do
// catálogo (gpt-5.x → Responses) não pode sobrepor uma OPENAI_API_URL
// apontando para outro host — o client Responses ignoraria essa URL e
// mandaria a key do gateway para api.openai.com.
func TestOpenAIPreferResponses(t *testing.T) {
	t.Run("official host follows catalog preference", func(t *testing.T) {
		setupTestEnv(t, map[string]string{"OPENAI_API_KEY": "fake-key"})
		assert.True(t, openAIPreferResponses(false, "gpt-5.4"))
	})

	t.Run("custom endpoint forces chat completions", func(t *testing.T) {
		setupTestEnv(t, map[string]string{
			"OPENAI_API_KEY": "fake-key",
			"OPENAI_API_URL": "https://api.z.ai/api/coding/paas/v4/chat/completions",
		})
		assert.False(t, openAIPreferResponses(false, "gpt-5.4"))
	})

	t.Run("explicit OPENAI_USE_RESPONSES wins over custom endpoint", func(t *testing.T) {
		setupTestEnv(t, map[string]string{
			"OPENAI_API_KEY":       "fake-key",
			"OPENAI_API_URL":       "https://gateway.example/v1/chat/completions",
			"OPENAI_USE_RESPONSES": "true",
		})
		assert.True(t, openAIPreferResponses(false, "gpt-5.4"))
	})

	t.Run("oauth always uses responses", func(t *testing.T) {
		setupTestEnv(t, map[string]string{
			"OPENAI_API_KEY": "fake-key",
			"OPENAI_API_URL": "https://gateway.example/v1/chat/completions",
		})
		assert.True(t, openAIPreferResponses(true, "gpt-5.4"))
	})

	t.Run("same-host path variant is not custom", func(t *testing.T) {
		setupTestEnv(t, map[string]string{
			"OPENAI_API_KEY": "fake-key",
			"OPENAI_API_URL": "https://api.openai.com/v2/chat/completions",
		})
		assert.True(t, openAIPreferResponses(false, "gpt-5.4"))
	})
}
