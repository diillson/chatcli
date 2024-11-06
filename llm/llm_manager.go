package llm

import (
	"fmt"
	"go.uber.org/zap"
	"os"
)

// ConfigError representa um erro de configuração, como variáveis de ambiente ausentes
type ConfigError struct {
	Message string
}

// Error implementa a interface de erro para ConfigError
func (e *ConfigError) Error() string {
	return fmt.Sprintf("ConfigError: %s", e.Message)
}

// LLMManager gerencia diferentes clientes LLM e o TokenManager
type LLMManager struct {
	clients      map[string]func(string) (LLMClient, error)
	logger       *zap.Logger
	tokenManager *TokenManager
}

// NewLLMManager cria uma nova instância de LLMManager com o TokenManager configurado.
// Recebe um logger, o nome do slug e o nome do tenant.
// Retorna uma instância de LLMManager ou um erro, caso ocorra.
func NewLLMManager(logger *zap.Logger, slugName, tenantName string) (*LLMManager, error) {
	manager := &LLMManager{
		clients: make(map[string]func(string) (LLMClient, error)),
		logger:  logger,
	}

	// Configurar o TokenManager
	if err := manager.configureTokenManager(slugName, tenantName); err != nil {
		return nil, err
	}

	// Configurar clientes LLM
	manager.configureOpenAIClient()
	manager.configureStackSpotClient(slugName)

	return manager, nil
}

// configureTokenManager configura o TokenManager com base nas variáveis de ambiente.
// Retorna um erro se CLIENT_ID ou CLIENT_SECRET não estiverem configurados.
func (m *LLMManager) configureTokenManager(slugName, tenantName string) error {
	clientID := os.Getenv("CLIENT_ID")
	clientSecret := os.Getenv("CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		return &ConfigError{Message: "CLIENT_ID ou CLIENT_SECRET não configurado"}
	}

	m.tokenManager = NewTokenManager(clientID, clientSecret, slugName, tenantName, m.logger)
	return nil
}

// configureOpenAIClient configura o cliente OpenAI se a variável de ambiente OPENAI_API_KEY estiver definida.
func (m *LLMManager) configureOpenAIClient() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey != "" {
		m.clients["OPENAI"] = func(model string) (LLMClient, error) {
			if model == "" {
				model = "gpt-4o-mini" // Modelo padrão
			}
			return NewOpenAIClient(apiKey, model, m.logger, 5, 30), nil
		}
	}
}

// configureStackSpotClient configura o cliente StackSpot.
func (m *LLMManager) configureStackSpotClient(slugName string) {
	m.clients["STACKSPOT"] = func(model string) (LLMClient, error) {
		return NewStackSpotClient(m.tokenManager, slugName, m.logger, 5, 30), nil
	}
}

// GetTokenManager retorna o TokenManager associado ao LLMManager.
// Retorna o TokenManager e um booleano indicando se o TokenManager está configurado.
func (m *LLMManager) GetTokenManager() (*TokenManager, bool) {
	return m.tokenManager, m.tokenManager != nil
}

// GetClient retorna um cliente LLM com base no provedor e no modelo especificados.
// Retorna um erro se o provedor não for suportado ou se houver falha na criação do cliente.
func (m *LLMManager) GetClient(provider string, model string) (LLMClient, error) {
	factoryFunc, ok := m.clients[provider]
	if !ok {
		return nil, fmt.Errorf("Provedor LLM '%s' não suportado", provider)
	}

	client, err := factoryFunc(model)
	if err != nil {
		m.logger.Error("Erro ao criar cliente LLM", zap.String("provider", provider), zap.Error(err))
		return nil, err
	}

	return client, nil
}
