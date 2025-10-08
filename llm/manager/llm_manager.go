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
}

// LLMManagerImpl gerencia diferentes clientes LLM e o TokenManager
type LLMManagerImpl struct {
	clients      map[string]func(string) (client.LLMClient, error)
	logger       *zap.Logger
	tokenManager token.Manager
}

// NewLLMManager cria uma nova instância de LLMManagerImpl.
func NewLLMManager(logger *zap.Logger, slugName, tenantName string) (LLMManager, error) {
	maxRetries := config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries)
	initialBackoff := config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)

	logger.Info("Política de Retry configurada",
		zap.Int("max_retries", maxRetries),
		zap.Duration("initial_backoff", initialBackoff))

	manager := &LLMManagerImpl{
		clients: make(map[string]func(string) (client.LLMClient, error)),
		logger:  logger,
	}

	// Configurar os providers
	manager.configurarOpenAIClient(maxRetries, initialBackoff)
	manager.configurarStackSpotClient(slugName, tenantName, maxRetries, initialBackoff)
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
		// NÃO logar a API key diretamente
		m.logger.Info("Configurando provedor Google AI",
			zap.Bool("api_key_present", true),
			zap.Int("api_key_length", len(apiKey))) // Apenas o tamanho, não o conteúdo

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
		// Cliente OpenAI padrão (chat completions ou responses)
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

		// Cliente OpenAI Assistente
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
func (m *LLMManagerImpl) configurarStackSpotClient(slugName, tenantName string, maxRetries int, initialBackoff time.Duration) {
	clientID := config.Global.GetString("CLIENT_ID")
	clientSecret := config.Global.GetString("CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		m.logger.Warn("CLIENT_ID ou CLIENT_SECRET não definidos, o provedor STACKSPOT não estará disponível")
		return
	}

	// NewTokenManager já retorna a interface token.Manager
	m.tokenManager = token.NewTokenManager(clientID, clientSecret, slugName, tenantName, m.logger)

	m.clients["STACKSPOT"] = func(model string) (client.LLMClient, error) {
		return stackspotai.NewStackSpotClient(m.tokenManager, slugName, m.logger, maxRetries, initialBackoff), nil
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
	// 1. Obter configurações do ConfigManager
	baseURL := config.Global.GetString("OLLAMA_BASE_URL")
	enable := config.Global.GetBool("OLLAMA_ENABLED", false) // Padrão é desativado se não definido

	// 2. Verificar se o provider está explicitamente desativado
	if !enable {
		m.logger.Info("OLLAMA_ENABLED não está ativo, provider Ollama ignorado.")
		return
	}

	// 3. Verificar a conectividade com o serviço Ollama
	// Cliente HTTP leve apenas para esta verificação
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

	// 4. Validar se há modelos disponíveis
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

	// 5. Se tudo estiver OK, registrar o factory do cliente
	m.logger.Info("Configurando provedor OLLAMA",
		zap.String("baseURL", baseURL),
		zap.Int("modelos_encontrados", len(tags.Models)),
	)

	m.clients["OLLAMA"] = func(model string) (client.LLMClient, error) {
		// Obter o modelo padrão do ConfigManager
		if model == "" {
			model = config.Global.GetString("OLLAMA_MODEL")
		}

		// Validar se o modelo solicitado existe
		found := false
		for _, m := range tags.Models {
			if m.Name == model {
				found = true
				break
			}
		}
		if !found {
			// Montar uma mensagem de erro útil
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

	// Agora que sabemos que o factory existe, podemos chamá-lo.
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
