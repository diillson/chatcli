package llm

import (
	"fmt"
	"go.uber.org/zap"
	"os"
)

type LLMManager struct {
	clients map[string]func(string) (LLMClient, error)
	logger  *zap.Logger
}

func NewLLMManager(logger *zap.Logger) (*LLMManager, error) {
	manager := &LLMManager{
		clients: make(map[string]func(string) (LLMClient, error)),
		logger:  logger,
	}

	// Configurar a fábrica para OpenAI
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		logger.Warn("OPENAI_API_KEY não está definido")
	} else {
		manager.clients["OPENAI"] = func(model string) (LLMClient, error) {
			if model == "" {
				model = "gpt-3.5-turbo" // Modelo padrão
			}
			return NewOpenAIClient(apiKey, model, logger), nil
		}
	}

	// Configurar a fábrica para StackSpot
	clientID := os.Getenv("CLIENT_ID")
	clientSecret := os.Getenv("CLIENT_SECRET")
	slug := os.Getenv("SLUG_NAME")
	if clientID == "" || clientSecret == "" || slug == "" {
		logger.Warn("As credenciais do StackSpot não estão definidas")
	} else {
		tokenManager := NewTokenManager(clientID, clientSecret, logger)
		manager.clients["STACKSPOT"] = func(model string) (LLMClient, error) {
			return NewStackSpotClient(tokenManager, slug, logger), nil
		}
	}

	return manager, nil
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
