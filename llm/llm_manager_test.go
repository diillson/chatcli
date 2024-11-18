package llm

import (
	"os"
	"testing"

	"go.uber.org/zap"
)

func TestNewLLMManager(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	// Configurar variáveis de ambiente para os testes
	os.Setenv("OPENAI_API_KEY", "test-openai-key")
	os.Setenv("CLIENT_ID", "test-client-id")
	os.Setenv("CLIENT_SECRET", "test-client-secret")
	os.Setenv("CLAUDEAI_API_KEY", "test-claudeai-key")

	manager, err := NewLLMManager(logger, "slug", "tenant")
	if err != nil {
		t.Fatalf("Erro ao criar LLMManager: %v", err)
	}

	providers := manager.GetAvailableProviders()
	if len(providers) != 3 {
		t.Errorf("Esperado 3 provedores, obtido %d", len(providers))
	}
}
