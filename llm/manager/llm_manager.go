/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package manager

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/claudeai"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/googleai"
	"github.com/diillson/chatcli/llm/ollama"
	"github.com/diillson/chatcli/llm/openai"
	"github.com/diillson/chatcli/llm/openai_assistant"
	"github.com/diillson/chatcli/llm/openai_responses"
	"github.com/diillson/chatcli/llm/stackspotai"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/llm/xai"
	"go.uber.org/zap"
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
	GetTokenManager() (token.Manager, bool)
	SetStackSpotRealm(realm string)
	SetStackSpotAgentID(agentID string)
	GetStackSpotRealm() string
	GetStackSpotAgentID() string
}

// LLMManagerImpl gerencia diferentes clientes LLM e o TokenManager
type LLMManagerImpl struct {
	clients          map[string]func(string) (client.LLMClient, error)
	logger           *zap.Logger
	tokenManager     token.Manager
	mu               sync.RWMutex
	stackspotRealm   string
	stackspotAgentID string
}

// NewLLMManager cria uma nova instância de LLMManagerImpl.
func NewLLMManager(logger *zap.Logger) (LLMManager, error) {
	maxRetries := config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries)
	initialBackoff := config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)

	logger.Info("Política de Retry configurada",
		zap.Int("max_retries", maxRetries),
		zap.Duration("initial_backoff", initialBackoff))

	manager := &LLMManagerImpl{
		clients:          make(map[string]func(string) (client.LLMClient, error)),
		logger:           logger,
		stackspotRealm:   config.Global.GetString("STACKSPOT_REALM"),
		stackspotAgentID: config.Global.GetString("STACKSPOT_AGENT_ID"),
	}

	manager.configurarOpenAIClient(maxRetries, initialBackoff)
	manager.configurarStackSpotClient(maxRetries, initialBackoff)
	manager.configurarClaudeAIClient(maxRetries, initialBackoff)
	manager.configurarGoogleAIClient(maxRetries, initialBackoff)
	manager.configurarXAIClient(maxRetries, initialBackoff)
	manager.configurarOllamaClient(maxRetries, initialBackoff)

	return manager, nil
}

// configurarGoogleAIClient configura o cliente Google AI (Gemini)
func (m *LLMManagerImpl) configurarGoogleAIClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("GOOGLEAI_API_KEY")
	if apiKey != "" {
		m.logger.Info("Configurando provedor Google AI",
			zap.Bool("api_key_present", true),
			zap.Int("api_key_length", len(apiKey)))

		m.clients["GOOGLEAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultGoogleAIModel
			}
			return googleai.NewGeminiClient(
				apiKey,
				model,
				m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}
	} else {
		m.logger.Warn("GOOGLEAI_API_KEY não definida, o provedor GOOGLEAI não estará disponível")
	}
}

// configurarOpenAIClient configura o cliente OpenAI se a variável de ambiente OPENAI_API_KEY estiver definida.
func (m *LLMManagerImpl) configurarOpenAIClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("OPENAI_API_KEY")
	if apiKey != "" {
		m.clients["OPENAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultOpenAIModel
			}

			useResponses := config.Global.GetBool("OPENAI_USE_RESPONSES", false)

			if !useResponses && catalog.GetPreferredAPI(catalog.ProviderOpenAI, model) == catalog.APIResponses {
				useResponses = true
			}

			if useResponses {
				m.logger.Info("Usando OpenAI Responses API", zap.String("model", model))
				return openai_responses.NewOpenAIResponsesClient(
					apiKey, model, m.logger,
					maxRetries,
					initialBackoff,
				), nil
			}

			m.logger.Info("Usando OpenAI Chat Completions API", zap.String("model", model))
			return openai.NewOpenAIClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil
		}

		m.clients["OPENAI_ASSISTANT"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultOpenAiAssistModel
			}
			return openai_assistant.NewOpenAIAssistantClient(apiKey, model, m.logger)
		}
	} else {
		m.logger.Warn("OPENAI_API_KEY não definida, o provedor OPENAI não estará disponível")
	}
}

// configurarStackSpotClient configura o cliente StackSpot
func (m *LLMManagerImpl) configurarStackSpotClient(maxRetries int, initialBackoff time.Duration) {
	clientID := config.Global.GetString("CLIENT_ID")
	clientKey := config.Global.GetString("CLIENT_KEY")

	// Se as credenciais existirem, o provedor será registrado.
	if clientID == "" || clientKey == "" {
		m.logger.Warn("CLIENT_ID ou CLIENT_KEY não definidos, o provedor STACKSPOT não estará disponível")
		return
	}

	m.mu.RLock()
	realm := m.stackspotRealm
	m.mu.RUnlock()

	// O TokenManager é criado, mesmo que o realm esteja vazio inicialmente.
	// Ele será atualizado via SetStackSpotRealm se necessário.
	m.tokenManager = token.NewTokenManager(clientID, clientKey, realm, m.logger)

	// A função de fábrica (factory) agora contém a verificação final.
	m.clients["STACKSPOT"] = func(model string) (client.LLMClient, error) {
		m.mu.RLock()
		currentRealm := m.stackspotRealm
		currentAgentID := m.stackspotAgentID
		m.mu.RUnlock()

		if currentRealm == "" || currentAgentID == "" {
			return nil, fmt.Errorf("provedor STACKSPOT requer STACKSPOT_REALM e STACKSPOT_AGENT_ID. Forneça-os no .env ou via flags --realm e --agent-id")
		}

		return stackspotai.NewStackSpotClient(m.tokenManager, currentAgentID, m.logger, maxRetries, initialBackoff), nil
	}
}

// configurarClaudeAIClient configura o cliente ClaudeAI
func (m *LLMManagerImpl) configurarClaudeAIClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("CLAUDEAI_API_KEY")
	if apiKey != "" {
		m.clients["CLAUDEAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultClaudeAIModel
			}
			return claudeai.NewClaudeClient(
				apiKey,
				model,
				m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}
	} else {
		m.logger.Warn("CLAUDEAI_API_KEY não definida, o provedor ClaudeAI não estará disponível")
	}
}

// configurarXAIClient configura o cliente xAI
func (m *LLMManagerImpl) configurarXAIClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("XAI_API_KEY")
	if apiKey != "" {
		m.logger.Info("Configurando provedor xAI")
		m.clients["XAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultXAIModel
			}
			return xai.NewXAIClient(
				apiKey,
				model,
				m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}
	} else {
		m.logger.Warn("XAI_API_KEY não definida, o provedor xAI não estará disponível")
	}
}

func (m *LLMManagerImpl) configurarOllamaClient(maxRetries int, initialBackoff time.Duration) {
	baseURL := config.Global.GetString("OLLAMA_BASE_URL")
	enable := config.Global.GetBool("OLLAMA_ENABLED", false)

	if !enable {
		m.logger.Info("OLLAMA_ENABLED não está ativo, provider Ollama ignorado.")
		return
	}

	hc := &http.Client{Timeout: 3 * time.Second}
	checkURL := strings.TrimRight(baseURL, "/") + "/api/tags"

	resp, err := hc.Get(checkURL)
	if err != nil {
		m.logger.Warn("Ollama não foi detectado no endereço configurado; o provider não estará disponível.",
			zap.String("baseURL", baseURL),
			zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		m.logger.Warn("Erro ao se comunicar com o serviço Ollama (verifique se está rodando).",
			zap.String("baseURL", baseURL),
			zap.Int("status_code", resp.StatusCode))
		return
	}

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		m.logger.Warn("Não foi possível decodificar a lista de modelos do Ollama.", zap.Error(err))
		return
	}
	if len(tags.Models) == 0 {
		m.logger.Warn("Nenhum modelo encontrado no serviço Ollama. Baixe um com 'ollama pull <nome_modelo>'.")
		return
	}

	m.logger.Info("Configurando provedor OLLAMA",
		zap.String("baseURL", baseURL),
		zap.Int("modelos_encontrados", len(tags.Models)),
	)

	m.clients["OLLAMA"] = func(model string) (client.LLMClient, error) {
		if model == "" {
			model = config.Global.GetString("OLLAMA_MODEL")
		}

		found := false
		for _, m := range tags.Models {
			if m.Name == model {
				found = true
				break
			}
		}
		if !found {
			var availableModels []string
			for _, m := range tags.Models {
				availableModels = append(availableModels, m.Name)
			}
			return nil, fmt.Errorf("modelo '%s' não encontrado no Ollama. Modelos disponíveis: %s", model, strings.Join(availableModels, ", "))
		}

		return ollama.NewClient(
			baseURL,
			model,
			m.logger,
			maxRetries,
			initialBackoff,
		), nil
	}
}

// GetAvailableProviders retorna uma lista de provedores disponíveis configurados
func (m *LLMManagerImpl) GetAvailableProviders() []string {
	var providers []string
	for provider := range m.clients {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

// GetClient retorna um cliente LLM com base no provedor e no modelo especificados.
func (m *LLMManagerImpl) GetClient(provider string, model string) (client.LLMClient, error) {
	factoryFunc, ok := m.clients[provider]
	if !ok {
		m.logger.Warn("Tentativa de obter cliente para provedor não configurado",
			zap.String("provider", provider))
		return nil, fmt.Errorf("erro: Provedor LLM '%s' não suportado ou não configurado", provider)
	}

	clientInstance, err := factoryFunc(model)
	if err != nil {
		m.logger.Error("Erro ao criar instância do cliente LLM",
			zap.String("provider", provider),
			zap.String("model", model),
			zap.Error(err))
		return nil, err
	}

	return clientInstance, nil
}

// GetTokenManager retorna o TokenManager se ele estiver configurado.
func (m *LLMManagerImpl) GetTokenManager() (token.Manager, bool) {
	return m.tokenManager, m.tokenManager != nil
}

// SetStackSpotRealm atualiza o realm em tempo de execução.
func (m *LLMManagerImpl) SetStackSpotRealm(realm string) {
	m.mu.Lock()
	m.stackspotRealm = realm
	m.mu.Unlock()
	if m.tokenManager != nil {
		m.tokenManager.SetRealm(realm)
	}
}

// SetStackSpotAgentID atualiza o agentID em tempo de execução.
func (m *LLMManagerImpl) SetStackSpotAgentID(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stackspotAgentID = agentID
}

// GetStackSpotRealm retorna o realm atual.
func (m *LLMManagerImpl) GetStackSpotRealm() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stackspotRealm
}

// GetStackSpotAgentID retorna o agentID atual.
func (m *LLMManagerImpl) GetStackSpotAgentID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stackspotAgentID
}
