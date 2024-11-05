package llm

import (
	"fmt"
	"go.uber.org/zap"
	"os"
)

type LLMManager struct {
	clients      map[string]func(string) (LLMClient, error)
	logger       *zap.Logger
	tokenManager *TokenManager
}

// NewLLMManager cria uma nova instância de LLMManager com TokenManager configurado
func NewLLMManager(logger *zap.Logger, slugName, tenantName string) (*LLMManager, error) {
	manager := &LLMManager{
		clients: make(map[string]func(string) (LLMClient, error)),
		logger:  logger,
	}

	// Configurar TokenManager com valores dinâmicos
	clientID := os.Getenv("CLIENT_ID")
	clientSecret := os.Getenv("CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("CLIENT_ID ou CLIENT_SECRET não configurado")
	}

	manager.tokenManager = NewTokenManager(clientID, clientSecret, slugName, tenantName, logger)

	// Configurar cliente para OpenAI
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey != "" {
		manager.clients["OPENAI"] = func(model string) (LLMClient, error) {
			if model == "" {
				model = "gpt-4o-mini" // Modelo padrão
			}
			return NewOpenAIClient(apiKey, model, logger), nil
		}
	}

	// Configurar cliente para StackSpot
	manager.clients["STACKSPOT"] = func(model string) (LLMClient, error) {
		return NewStackSpotClient(manager.tokenManager, slugName, logger), nil
	}

	return manager, nil
}

func (m *LLMManager) GetTokenManager() (*TokenManager, bool) {
	return m.tokenManager, m.tokenManager != nil
}

func (m *LLMManager) GetClient(provider string, model string) (LLMClient, error) {
	factoryFunc, ok := m.clients[provider]
	if !ok {
		return nil, fmt.Errorf("Provedor LLM '%s' não suportado", provider)
	}
	client, err := factoryFunc(model)
	if err != nil {
		return nil, err
	}
	return client, nil
}
