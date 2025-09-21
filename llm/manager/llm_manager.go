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
	"os"
	"sort"
	"strconv"
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
	"github.com/diillson/chatcli/utils"
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
	// Ler configs de retry de ENV ou usar defaults
	maxRetries := config.DefaultMaxRetries
	if v := os.Getenv("MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxRetries = n
			logger.Info("Usando MAX_RETRIES de ENV", zap.Int("max_retries", maxRetries))
		}
	}

	initialBackoff := config.DefaultInitialBackoff
	if v := os.Getenv("INITIAL_BACKOFF"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			initialBackoff = d
			logger.Info("Usando INITIAL_BACKOFF de ENV", zap.Duration("initial_backoff", initialBackoff))
		}
	}

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
	apiKey := os.Getenv("GOOGLEAI_API_KEY")
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
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey != "" {
		// Cliente OpenAI padrão (chat completions ou responses)
		m.clients["OPENAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultOpenAIModel
			}

			// Seleção entre Chat Completions e Responses API
			useResponses := false

			// 1) Flag/env explícita
			if v := os.Getenv("OPENAI_USE_RESPONSES"); strings.EqualFold(v, "true") {
				useResponses = true
			}

			// 2) Preferência do registry (ex.: GPT-5)
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
	clientID := os.Getenv("CLIENT_ID")
	clientSecret := os.Getenv("CLIENT_SECRET")

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
	apiKey := os.Getenv("CLAUDEAI_API_KEY")
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
	apiKey := os.Getenv("XAI_API_KEY")
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
	baseURL := utils.GetEnvOrDefault("OLLAMA_BASE_URL", config.OllamaDefaultBaseURL)
	enable := strings.EqualFold(os.Getenv("OLLAMA_ENABLED"), "true")

	if !enable {
		m.logger.Info("OLLAMA_ENABLED não está ativo, provider ignorado")
		return
	}

	// Cliente HTTP para checar serviço
	hc := utils.NewHTTPClient(m.logger, 2*time.Second)
	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/tags", nil)
	resp, err := hc.Do(req)
	if err != nil {
		m.logger.Warn("Ollama não detectado; provider não será listado",
			zap.String("baseURL", baseURL), zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		m.logger.Warn("Erro ao listar modelos do Ollama",
			zap.Int("status", resp.StatusCode))
		return
	}

	// Validar se tem modelos disponíveis
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		m.logger.Warn("Não foi possível decodificar resposta do Ollama", zap.Error(err))
		return
	}
	if len(tags.Models) == 0 {
		m.logger.Warn("Nenhum modelo encontrado no Ollama")
		return
	}

	// Registrar client
	m.logger.Info("Configurando provedor OLLAMA",
		zap.String("baseURL", baseURL),
		zap.Int("model_count", len(tags.Models)),
	)

	m.clients["OLLAMA"] = func(model string) (client.LLMClient, error) {
		if model == "" {
			model = config.DefaultOllamaModel
		}

		// valida se o modelo existe nos tags
		found := false
		for _, m := range tags.Models {
			if m.Name == model {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("modelo '%s' não encontrado no Ollama", model)
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
		return nil, fmt.Errorf("erro: Provedor LLM '%s' não suportado ou não configurado", provider)
	}

	client, err := factoryFunc(model)
	if err != nil {
		m.logger.Error("Erro ao criar cliente LLM", zap.String("provider", provider), zap.Error(err))
		return nil, err
	}

	return client, nil
}

// GetTokenManager retorna o TokenManager se ele estiver configurado.
func (m *LLMManagerImpl) GetTokenManager() (token.Manager, bool) {
	return m.tokenManager, m.tokenManager != nil
}
