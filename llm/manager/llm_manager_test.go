package manager

import (
	"os"
	"testing"

	"github.com/diillson/chatcli/llm/openai"
	"github.com/diillson/chatcli/llm/stackspotai"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestLLMManager_GetClient(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	t.Run("OpenAI Client Success", func(t *testing.T) {
		os.Setenv("OPENAI_API_KEY", "fake-key")
		defer os.Unsetenv("OPENAI_API_KEY")

		mgr, err := NewLLMManager(logger, "", "")
		assert.NoError(t, err)

		client, err := mgr.GetClient("OPENAI", "gpt-4o")
		assert.NoError(t, err)
		assert.IsType(t, &openai.OpenAIClient{}, client)
	})

	t.Run("StackSpot Client Success", func(t *testing.T) {
		os.Setenv("CLIENT_ID", "fake-id")
		os.Setenv("CLIENT_SECRET", "fake-secret")
		defer os.Unsetenv("CLIENT_ID")
		defer os.Unsetenv("CLIENT_SECRET")

		mgr, err := NewLLMManager(logger, "test-slug", "test-tenant")
		assert.NoError(t, err)

		client, err := mgr.GetClient("STACKSPOT", "")
		assert.NoError(t, err)
		assert.IsType(t, &stackspotai.StackSpotClient{}, client)
	})

	t.Run("Unsupported Provider", func(t *testing.T) {
		mgr, err := NewLLMManager(logger, "", "")
		assert.NoError(t, err)

		_, err = mgr.GetClient("BARD", "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "não suportado ou não configurado")
	})

	t.Run("Provider not configured", func(t *testing.T) {
		// Garante que a chave não está setada
		os.Unsetenv("OPENAI_API_KEY")

		mgr, err := NewLLMManager(logger, "", "")
		assert.NoError(t, err)

		_, err = mgr.GetClient("OPENAI", "gpt-4o")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "não suportado ou não configurado")
	})
}

func TestLLMManager_GetAvailableProviders(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Caso 1: Nenhum configurado
	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("CLIENT_ID")
	mgr, _ := NewLLMManager(logger, "", "")
	providers := mgr.GetAvailableProviders()
	assert.Empty(t, providers)

	// Caso 2: Apenas OpenAI
	os.Setenv("OPENAI_API_KEY", "fake-key")
	mgr, _ = NewLLMManager(logger, "", "")
	providers = mgr.GetAvailableProviders()
	assert.Contains(t, providers, "OPENAI")
	assert.Contains(t, providers, "OPENAI_ASSISTANT")
	assert.Len(t, providers, 2)
	os.Unsetenv("OPENAI_API_KEY")
}
