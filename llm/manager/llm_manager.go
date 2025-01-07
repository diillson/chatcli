package manager

import (
	"fmt"
	"github.com/diillson/chatcli/llm/claudeai"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/openai"
	"github.com/diillson/chatcli/llm/stackspotai"
	"github.com/diillson/chatcli/llm/token"
	"go.uber.org/zap"
	"os"
)

const (
	defaultOpenAIModel   = "gpt-4o-mini"
	defaultClaudeAIModel = "claude-3-5-sonnet-20241022"
)

// ConfigError representa um erro de configuração, como variáveis de ambiente ausentes
type ConfigError struct {
	Mensagem string
}

// Error implementa a interface de erro para ConfigError
func (e *ConfigError) Error() string {
	return fmt.Sprintf("ConfigError: %s", e.Mensagem)
}

// LLMManager é a interface que define os métodos que o gerenciador de LLMs deve implementar
type LLMManager interface {
	GetClient(provider string, model string) (client.LLMClient, error)
	GetAvailableProviders() []string
	GetTokenManager() (*token.TokenManager, bool)
}

// LLMManagerImpl gerencia diferentes clientes LLM e o TokenManager
type LLMManagerImpl struct {
	clients      map[string]func(string) (client.LLMClient, error)
	logger       *zap.Logger
	tokenManager *token.TokenManager
}

// NewLLMManager cria uma nova instância de LLMManagerImpl.
func NewLLMManager(logger *zap.Logger, slugName, tenantName string) (LLMManager, error) {
	manager := &LLMManagerImpl{
		clients: make(map[string]func(string) (client.LLMClient, error)),
		logger:  logger,
	}

	// Configurar os providers
	manager.configurarOpenAIClient()
	manager.configurarStackSpotClient(slugName, tenantName)
	manager.configurarClaudeAIClient()

	return manager, nil
}

// configurarOpenAIClient configura o cliente OpenAI se a variável de ambiente OPENAI_API_KEY estiver definida.
func (m *LLMManagerImpl) configurarOpenAIClient() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey != "" {
		m.clients["OPENAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = defaultOpenAIModel
			}
			return openai.NewOpenAIClient(apiKey, model, m.logger, 50, 300), nil
		}
	} else {
		m.logger.Warn("OPENAI_API_KEY não definida, o provedor OPENAI não estará disponível")
	}
}

// configurarStackSpotClient configura o cliente StackSpot se as variáveis de ambiente necessárias estiverem definidas.
func (m *LLMManagerImpl) configurarStackSpotClient(slugName, tenantName string) {
	clientID := os.Getenv("CLIENT_ID")
	clientSecret := os.Getenv("CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		m.logger.Warn("CLIENT_ID ou CLIENT_SECRET não definidos, o provedor STACKSPOT não estará disponível")
		return
	}

	// Inicializar o TokenManager
	m.tokenManager = token.NewTokenManager(clientID, clientSecret, slugName, tenantName, m.logger)

	// Configurar o cliente StackSpot
	m.clients["STACKSPOT"] = func(model string) (client.LLMClient, error) {
		return stackspotai.NewStackSpotClient(m.tokenManager, slugName, m.logger, 50, 300), nil
	}
}

// configurarClaudeAIClient configura o cliente ClaudeAI se a variável de ambiente CLAUDEAI_API_KEY estiver definida.
func (m *LLMManagerImpl) configurarClaudeAIClient() {
	apiKey := os.Getenv("CLAUDEAI_API_KEY")
	if apiKey != "" {
		m.clients["CLAUDEAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = defaultClaudeAIModel
			}
			return claudeai.NewClaudeClient(apiKey, model, m.logger), nil
		}
	} else {
		m.logger.Warn("CLAUDEAI_API_KEY não definida, o provedor ClaudeAI não estará disponível")
	}
}

// GetAvailableProviders retorna uma lista de provedores disponíveis configurados
func (m *LLMManagerImpl) GetAvailableProviders() []string {
	var providers []string
	for provider := range m.clients {
		providers = append(providers, provider)
	}
	return providers
}

// GetClient retorna um cliente LLM com base no provedor e no modelo especificados.
func (m *LLMManagerImpl) GetClient(provider string, model string) (client.LLMClient, error) {
	factoryFunc, ok := m.clients[provider]
	if !ok {
		return nil, fmt.Errorf("Erro: Provedor LLM '%s' não suportado ou não configurado", provider)
	}

	client, err := factoryFunc(model)
	if err != nil {
		m.logger.Error("Erro ao criar cliente LLM", zap.String("provider", provider), zap.Error(err))
		return nil, err
	}

	return client, nil
}

// GetTokenManager retorna o TokenManager se ele estiver configurado.
func (m *LLMManagerImpl) GetTokenManager() (*token.TokenManager, bool) {
	return m.tokenManager, m.tokenManager != nil
}
