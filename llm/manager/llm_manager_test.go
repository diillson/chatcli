package manager

import (
	"os"
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/openai"
	"github.com/diillson/chatcli/llm/stackspotai"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func setupTestEnv(t *testing.T, envs map[string]string) {
	originalEnvs := make(map[string]string)

	keysToClear := []string{
		"OPENAI_API_KEY", "CLIENT_ID", "CLIENT_KEY",
		"CLAUDEAI_API_KEY", "GOOGLEAI_API_KEY", "XAI_API_KEY",
		"OLLAMA_ENABLED", "OLLAMA_BASE_URL",
		"STACKSPOT_REALM", "STACKSPOT_AGENT_ID",
	}

	for _, key := range keysToClear {
		if val, ok := os.LookupEnv(key); ok {
			originalEnvs[key] = val
		}
		os.Unsetenv(key)
	}

	for key, value := range envs {
		os.Setenv(key, value)
	}

	logger, _ := zap.NewDevelopment()
	config.Global = config.New(logger)
	config.Global.Load()

	t.Cleanup(func() {
		for key := range envs {
			os.Unsetenv(key)
		}
		for key, value := range originalEnvs {
			os.Setenv(key, value)
		}
		config.Global = config.New(logger)
		config.Global.Load()
	})
}

func TestLLMManager_GetClient(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	t.Run("OpenAI Client Success", func(t *testing.T) {
		setupTestEnv(t, map[string]string{"OPENAI_API_KEY": "fake-key"})

		mgr, err := NewLLMManager(logger)
		assert.NoError(t, err)

		client, err := mgr.GetClient("OPENAI", "gpt-4o")
		assert.NoError(t, err)
		assert.NotNil(t, client)
		assert.IsType(t, &openai.OpenAIClient{}, client)
	})

	t.Run("StackSpot Client Success", func(t *testing.T) {
		// CORRIGIDO: Usa CLIENT_KEY
		setupTestEnv(t, map[string]string{
			"CLIENT_ID":          "fake-id",
			"CLIENT_KEY":         "fake-key",
			"STACKSPOT_REALM":    "test-realm",
			"STACKSPOT_AGENT_ID": "test-agent-id",
		})

		mgr, err := NewLLMManager(logger)
		assert.NoError(t, err)

		client, err := mgr.GetClient("STACKSPOT", "")
		assert.NoError(t, err)
		assert.NotNil(t, client)
		assert.IsType(t, &stackspotai.StackSpotClient{}, client)
	})

	t.Run("Unsupported Provider", func(t *testing.T) {
		setupTestEnv(t, nil)

		mgr, err := NewLLMManager(logger)
		assert.NoError(t, err)

		client, err := mgr.GetClient("BARD", "")
		assert.Error(t, err)
		assert.Nil(t, client)
		assert.Contains(t, err.Error(), "não suportado ou não configurado")
	})

	t.Run("Provider not configured", func(t *testing.T) {
		setupTestEnv(t, nil)

		mgr, err := NewLLMManager(logger)
		assert.NoError(t, err)

		client, err := mgr.GetClient("OPENAI", "gpt-4o")
		assert.Error(t, err)
		assert.Nil(t, client)
		assert.Contains(t, err.Error(), "não suportado ou não configurado")
	})
}

func TestLLMManager_GetAvailableProviders(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	t.Run("No providers configured", func(t *testing.T) {
		setupTestEnv(t, nil)
		mgr, _ := NewLLMManager(logger)
		providers := mgr.GetAvailableProviders()
		assert.Empty(t, providers)
	})

	t.Run("Only OpenAI configured", func(t *testing.T) {
		setupTestEnv(t, map[string]string{"OPENAI_API_KEY": "fake-key"})
		mgr, _ := NewLLMManager(logger)
		providers := mgr.GetAvailableProviders()
		assert.ElementsMatch(t, []string{"OPENAI", "OPENAI_ASSISTANT"}, providers)
	})

	t.Run("OpenAI and StackSpot configured", func(t *testing.T) {
		// CORRIGIDO: Usa CLIENT_KEY
		setupTestEnv(t, map[string]string{
			"OPENAI_API_KEY":     "fake-key",
			"CLIENT_ID":          "fake-id",
			"CLIENT_KEY":         "fake-key",
			"STACKSPOT_REALM":    "test-realm",
			"STACKSPOT_AGENT_ID": "test-agent-id",
		})
		mgr, _ := NewLLMManager(logger)
		providers := mgr.GetAvailableProviders()
		assert.ElementsMatch(t, []string{"OPENAI", "OPENAI_ASSISTANT", "STACKSPOT"}, providers)
	})
}
