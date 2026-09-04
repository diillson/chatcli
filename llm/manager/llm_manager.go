/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/bedrock"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/claudeai"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/copilot"
	"github.com/diillson/chatcli/llm/devincli"
	githubmodels "github.com/diillson/chatcli/llm/githubmodels"
	"github.com/diillson/chatcli/llm/googleai"
	"github.com/diillson/chatcli/llm/minimax"
	"github.com/diillson/chatcli/llm/moonshot"
	"github.com/diillson/chatcli/llm/ollama"
	"github.com/diillson/chatcli/llm/openai"
	"github.com/diillson/chatcli/llm/openaiassistant"
	"github.com/diillson/chatcli/llm/openairesponses"
	"github.com/diillson/chatcli/llm/openrouter"
	"github.com/diillson/chatcli/llm/ratelimit"
	"github.com/diillson/chatcli/llm/stackspotai"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/llm/xai"
	"github.com/diillson/chatcli/llm/zai"
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
	SetStackSpotRealm(realm string)
	SetStackSpotAgentID(agentID string)
	GetStackSpotRealm() string
	GetStackSpotAgentID() string
	RefreshProviders()
	// CreateClientWithKey creates an LLM client using a caller-provided API key
	// instead of the server's default credentials. Used for client-forwarded tokens.
	CreateClientWithKey(provider, model, apiKey string) (client.LLMClient, error)
	// CreateClientWithConfig creates an LLM client using caller-provided credentials
	// plus provider-specific configuration. Supports all providers including StackSpot
	// (needs client_id, client_key, realm, agent_id) and Ollama (needs base_url).
	CreateClientWithConfig(provider, model, apiKey string, providerConfig map[string]string) (client.LLMClient, error)
	// ListModelsForProvider lists available models for a provider, either dynamically
	// from the provider's API (if supported) or from the static catalog.
	ListModelsForProvider(ctx context.Context, provider string) ([]client.ModelInfo, error)
}

// LLMManagerImpl gerencia diferentes clientes LLM e o TokenManager
type LLMManagerImpl struct {
	clients          map[string]func(string) (client.LLMClient, error)
	logger           *zap.Logger
	tokenManager     token.Manager
	mu               sync.RWMutex
	stackspotRealm   string
	stackspotAgentID string

	// baseCtx is the manager-lifetime context used for request-independent
	// work such as resolving (and refreshing) auth providers. It is derived
	// from the constructor's context with cancellation detached, since the
	// manager and its token providers outlive any single request.
	baseCtx context.Context

	tpMu           sync.Mutex
	tokenProviders map[auth.ProviderID]auth.TokenProvider
}

// tokenProviderFor returns a cached TokenProvider for the given auth provider,
// resolving it on first use. The returned provider owns a background refresh
// goroutine that lives for the lifetime of the manager (closed on
// RefreshProviders or Close).
func (m *LLMManagerImpl) tokenProviderFor(provider auth.ProviderID) (auth.TokenProvider, error) {
	m.tpMu.Lock()
	defer m.tpMu.Unlock()
	if m.tokenProviders == nil {
		m.tokenProviders = make(map[auth.ProviderID]auth.TokenProvider)
	}
	if tp, ok := m.tokenProviders[provider]; ok {
		return tp, nil
	}
	tp, err := auth.ResolveAuthProvider(m.baseCtx, provider, m.logger)
	if err != nil {
		return nil, err
	}
	m.tokenProviders[provider] = tp
	return tp, nil
}

// closeTokenProviders releases every cached TokenProvider. Called when
// credentials change at runtime (RefreshProviders) so the next resolve picks
// up the new store contents.
func (m *LLMManagerImpl) closeTokenProviders() {
	m.tpMu.Lock()
	for _, tp := range m.tokenProviders {
		tp.Close()
	}
	m.tokenProviders = make(map[auth.ProviderID]auth.TokenProvider)
	m.tpMu.Unlock()
}

// Close releases all resources owned by the manager (background refresh
// goroutines, etc.). Safe to call multiple times.
func (m *LLMManagerImpl) Close() {
	m.closeTokenProviders()
}

// NewLLMManager cria uma nova instância de LLMManagerImpl.
func NewLLMManager(logger *zap.Logger) (LLMManager, error) {
	maxRetries := config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries)
	initialBackoff := config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)

	logger.Info(i18n.T("llm.manager.retry_policy"),
		zap.Int("max_retries", maxRetries),
		zap.Duration("initial_backoff", initialBackoff))

	// Record rate-limit headers for EVERY HTTP provider via the single
	// auth.DoWithRefresh seam (provider-agnostic; no per-client wiring).
	auth.ResponseObserver = ratelimit.Record

	manager := &LLMManagerImpl{
		clients:          make(map[string]func(string) (client.LLMClient, error)),
		logger:           logger,
		stackspotRealm:   config.Global.GetString("STACKSPOT_REALM"),
		stackspotAgentID: config.Global.GetString("STACKSPOT_AGENT_ID"),
		// Auth providers (and their refresh goroutines) live for the manager's
		// lifetime and are request-independent, so a fresh background root is the
		// correct parent for their resolution/refresh work.
		baseCtx:        context.Background(),
		tokenProviders: make(map[auth.ProviderID]auth.TokenProvider),
	}

	manager.configurarOpenAIClient(maxRetries, initialBackoff)
	manager.configurarStackSpotClient(maxRetries, initialBackoff)
	manager.configurarClaudeAIClient(maxRetries, initialBackoff)
	manager.configurarGoogleAIClient(maxRetries, initialBackoff)
	manager.configurarXAIClient(maxRetries, initialBackoff)
	manager.configurarZAIClient(maxRetries, initialBackoff)
	manager.configurarMiniMaxClient(maxRetries, initialBackoff)
	manager.configurarMoonshotClient(maxRetries, initialBackoff)
	manager.configurarOllamaClient(maxRetries, initialBackoff)
	manager.configurarCopilotClient(maxRetries, initialBackoff)
	manager.configurarGitHubModelsClient(maxRetries, initialBackoff)
	manager.configurarOpenRouterClient(maxRetries, initialBackoff)
	manager.configurarBedrockClient(maxRetries, initialBackoff)
	manager.configurarDevinCLIClient(maxRetries, initialBackoff)

	return manager, nil
}

// configurarBedrockClient registra o provedor AWS Bedrock quando há sinal de
// configuração AWS (env vars, AWS_PROFILE, ou ~/.aws/credentials). A resolução
// real das credenciais acontece apenas na primeira chamada (credentials chain
// padrão: env → shared config → IAM role).
func (m *LLMManagerImpl) configurarBedrockClient(maxRetries int, initialBackoff time.Duration) {
	if !bedrock.CredentialsAvailable() {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "AWS credentials", "BEDROCK"))
		return
	}
	profile, profileSource := bedrock.ResolveProfile()
	// The effective identity is logged at boot on purpose: "Bedrock answered
	// as the wrong account" is otherwise invisible until an API call fails,
	// and on an editor-spawned acp/mcp-server the profile silently missing
	// from the environment is exactly the bug this line exposes.
	m.logger.Info(i18n.T("llm.info.configuring_provider", "AWS Bedrock"),
		zap.String("profile", profile),
		zap.String("profile_source", profileSource),
		zap.String("dotenv", config.ActiveDotenv().Path))
	m.clients["BEDROCK"] = func(model string) (client.LLMClient, error) {
		if model == "" {
			model = resolveBedrockModel()
		}
		region, _ := bedrock.ResolveRegion()
		if region == "" {
			region = config.DefaultBedrockRegion
		}
		profile, _ := bedrock.ResolveProfile()
		return bedrock.NewBedrockClient(model, region, profile, m.logger, maxRetries, initialBackoff), nil
	}
}

// resolveModelEnv returns the model to use when the caller did not pick
// one explicitly: the given env var from the process environment first,
// then the persisted config (.env / config file), then the provider's
// default. Catalog aliases work as values ("claude-opus-4-8" resolves to
// the invokable global profile id).
func resolveModelEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if config.Global != nil {
		if v := strings.TrimSpace(config.Global.GetString(key)); v != "" {
			return v
		}
	}
	return def
}

// resolveBedrockModel keeps the historical name for the Bedrock call sites.
func resolveBedrockModel() string {
	return resolveModelEnv("BEDROCK_MODEL", config.DefaultBedrockModel)
}

// configurarGoogleAIClient configura o cliente Google AI (Gemini)
func (m *LLMManagerImpl) configurarGoogleAIClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("GOOGLEAI_API_KEY")
	if apiKey != "" {
		m.logger.Info(i18n.T("llm.info.configuring_provider", "Google AI"),
			zap.Bool("api_key_present", true),
			zap.Int("api_key_length", len(apiKey)))

		m.clients["GOOGLEAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultGoogleAIModel
			}
			provider := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
			return googleai.NewGeminiClient(
				provider,
				model,
				m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}
	} else {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "GOOGLEAI_API_KEY", "GOOGLEAI"))
	}
}

// configurarOpenAIClient configura o cliente OpenAI se a variável de ambiente OPENAI_API_KEY estiver definida.
// The factory picks `openairesponses` (ChatGPT backend) for OAuth tokens and
// falls back to chat-completions for API keys; both flavors share the same
// cached TokenProvider so the OAuth refresh loop runs only once.
func (m *LLMManagerImpl) configurarOpenAIClient(maxRetries int, initialBackoff time.Duration) {
	if _, err := m.tokenProviderFor(auth.ProviderOpenAI); err != nil {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "OPENAI_API_KEY", "OPENAI"), zap.Error(err))
		return
	}
	m.clients["OPENAI"] = func(model string) (client.LLMClient, error) {
		tp, err := m.tokenProviderFor(auth.ProviderOpenAI)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "OpenAI"), err)
		}
		if model == "" {
			model = config.DefaultOpenAIModel
		}

		// OAuth tokens always use the Responses API (ChatGPT backend only speaks Responses format)
		isOAuth := tp.Mode() == auth.AuthModeOAuth

		useResponses := openAIPreferResponses(isOAuth, model)

		if useResponses {
			m.logger.Info(i18n.T("llm.manager.using_responses_api"), zap.String("model", model), zap.Bool("oauth", isOAuth))
			return openairesponses.NewOpenAIResponsesClient(
				tp, model, m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}

		m.logger.Info(i18n.T("llm.manager.using_chat_completions"), zap.String("model", model))
		return openai.NewOpenAIClient(tp, model, m.logger, maxRetries, initialBackoff), nil
	}

	m.clients["OPENAI_ASSISTANT"] = func(model string) (client.LLMClient, error) {
		tp, err := m.tokenProviderFor(auth.ProviderOpenAI)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "OpenAI"), err)
		}
		if model == "" {
			model = config.DefaultOpenAiAssistModel
		}
		return openaiassistant.NewOpenAIAssistantClient(m.baseCtx, tp, model, m.logger)
	}
}

// openAIPreferResponses decide entre as superfícies Responses e
// chat-completions do provider OPENAI. A preferência do catálogo (gpt-5.x →
// Responses) só vale no host oficial: uma OPENAI_API_URL custom aponta para
// um gateway OpenAI-compatible que fala chat-completions, e o client
// Responses ignoraria essa URL — mandando a key do gateway para
// api.openai.com (401 garantido). OPENAI_USE_RESPONSES=true explícito ainda
// vence, para gateways que exponham a Responses API (requer
// OPENAI_RESPONSES_API_URL apontando para eles).
func openAIPreferResponses(isOAuth bool, model string) bool {
	if isOAuth || config.Global.GetBool("OPENAI_USE_RESPONSES", false) {
		return true
	}
	chatURL := utils.GetEnvOrDefault("OPENAI_API_URL", config.OpenAIAPIURL)
	if utils.IsCustomEndpoint(chatURL, config.OpenAIAPIURL) {
		return false
	}
	return catalog.GetPreferredAPI(catalog.ProviderOpenAI, model) == catalog.APIResponses
}

// configurarStackSpotClient configura o cliente StackSpot
func (m *LLMManagerImpl) configurarStackSpotClient(maxRetries int, initialBackoff time.Duration) {
	clientID := config.Global.GetString("CLIENT_ID")
	clientKey := config.Global.GetString("CLIENT_KEY")

	// Se as credenciais existirem, o provedor será registrado.
	if clientID == "" || clientKey == "" {
		m.logger.Warn(i18n.T("llm.manager.stackspot_credentials_missing"))
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
			return nil, errors.New(i18n.T("llm.manager.stackspot_requires_config"))
		}

		return stackspotai.NewStackSpotClient(m.tokenManager, currentAgentID, m.logger, maxRetries, initialBackoff), nil
	}
}

// configurarClaudeAIClient configura o cliente ClaudeAI
func (m *LLMManagerImpl) configurarClaudeAIClient(maxRetries int, initialBackoff time.Duration) {
	if _, err := m.tokenProviderFor(auth.ProviderAnthropic); err != nil {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "ANTHROPIC_API_KEY", "CLAUDEAI"), zap.Error(err))
		return
	}
	m.clients["CLAUDEAI"] = func(model string) (client.LLMClient, error) {
		tp, err := m.tokenProviderFor(auth.ProviderAnthropic)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "Anthropic"), err)
		}
		if model == "" {
			model = config.DefaultClaudeAIModel
		}
		return claudeai.NewClaudeClient(
			tp,
			model,
			m.logger,
			maxRetries,
			initialBackoff,
		), nil
	}
}

// configurarXAIClient configura o cliente xAI
func (m *LLMManagerImpl) configurarXAIClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("XAI_API_KEY")
	if apiKey != "" {
		m.logger.Info(i18n.T("llm.info.configuring_provider", "xAI"))
		m.clients["XAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultXAIModel
			}
			provider := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
			return xai.NewXAIClient(
				provider,
				model,
				m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}
	} else {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "XAI_API_KEY", "xAI"))
	}
}

func (m *LLMManagerImpl) configurarZAIClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("ZAI_API_KEY")
	if apiKey != "" {
		m.logger.Info(i18n.T("llm.info.configuring_provider", "ZAI (Zhipu AI)"))
		m.clients["ZAI"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultZAIModel
			}
			provider := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
			return zai.NewZAIClient(
				m.baseCtx,
				provider,
				model,
				m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}
	} else {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "ZAI_API_KEY", "ZAI"))
	}
}

func (m *LLMManagerImpl) configurarMoonshotClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("MOONSHOT_API_KEY")
	if apiKey != "" {
		m.logger.Info(i18n.T("llm.info.configuring_provider", "Moonshot (Kimi)"))
		m.clients["MOONSHOT"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultMoonshotModel
			}
			provider := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
			return moonshot.NewMoonshotClient(
				provider,
				model,
				m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}
	} else {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "MOONSHOT_API_KEY", "MOONSHOT"))
	}
}

func (m *LLMManagerImpl) configurarMiniMaxClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("MINIMAX_API_KEY")
	if apiKey != "" {
		m.logger.Info(i18n.T("llm.info.configuring_provider", "MiniMax"))
		m.clients["MINIMAX"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultMiniMaxModel
			}
			provider := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
			return minimax.NewMiniMaxClient(
				provider,
				model,
				m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}
	} else {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "MINIMAX_API_KEY", "MiniMax"))
	}
}

func (m *LLMManagerImpl) configurarOllamaClient(maxRetries int, initialBackoff time.Duration) {
	baseURL := config.Global.GetString("OLLAMA_BASE_URL")
	enable := config.Global.GetBool("OLLAMA_ENABLED", false)

	if !enable {
		m.logger.Info(i18n.T("llm.manager.ollama_not_active"))
		return
	}

	hc := &http.Client{Timeout: 3 * time.Second}
	checkURL := strings.TrimRight(baseURL, "/") + "/api/tags"

	resp, err := hc.Get(checkURL)
	if err != nil {
		m.logger.Warn(i18n.T("llm.manager.ollama_not_detected"),
			zap.String("baseURL", baseURL),
			zap.Error(err))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		m.logger.Warn(i18n.T("llm.manager.ollama_comm_error"),
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
		m.logger.Warn(i18n.T("llm.manager.ollama_decode_error"), zap.Error(err))
		return
	}
	if len(tags.Models) == 0 {
		m.logger.Warn(i18n.T("llm.manager.ollama_no_models"))
		return
	}

	m.logger.Info(i18n.T("llm.info.configuring_provider", "OLLAMA"),
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
			return nil, fmt.Errorf("%s", i18n.T("llm.manager.model_not_found_ollama", model, strings.Join(availableModels, ", ")))
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

// configurarCopilotClient configura o cliente GitHub Copilot.
//
// closure targets different provider packages; shared boilerplate is
// kept readable at the call site rather than abstracted.
//
//nolint:dupl // near-duplicate of configurarGitHubModelsClient but the
func (m *LLMManagerImpl) configurarCopilotClient(maxRetries int, initialBackoff time.Duration) {
	if _, err := m.tokenProviderFor(auth.ProviderGitHubCopilot); err != nil {
		m.logger.Info(i18n.T("llm.warn.provider_not_configured", "GitHub Copilot", "COPILOT"), zap.Error(err))
		return
	}
	m.logger.Info(i18n.T("llm.info.configuring_provider", "GitHub Copilot"))
	m.clients["COPILOT"] = func(model string) (client.LLMClient, error) {
		tp, err := m.tokenProviderFor(auth.ProviderGitHubCopilot)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "GitHub Copilot"), err)
		}
		if model == "" {
			model = config.DefaultCopilotModel
		}
		return copilot.NewClient(tp, model, m.logger, maxRetries, initialBackoff), nil
	}
}

// configurarGitHubModelsClient configura o cliente GitHub Models marketplace.
//
//nolint:dupl // near-duplicate of configurarCopilotClient; see note there.
func (m *LLMManagerImpl) configurarGitHubModelsClient(maxRetries int, initialBackoff time.Duration) {
	if _, err := m.tokenProviderFor(auth.ProviderGitHubModels); err != nil {
		m.logger.Info(i18n.T("llm.warn.provider_not_configured", "GitHub Models", "GITHUB_MODELS"), zap.Error(err))
		return
	}
	m.logger.Info(i18n.T("llm.info.configuring_provider", "GitHub Models"))
	m.clients["GITHUB_MODELS"] = func(model string) (client.LLMClient, error) {
		tp, err := m.tokenProviderFor(auth.ProviderGitHubModels)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "GitHub Models"), err)
		}
		if model == "" {
			model = config.DefaultGitHubModelsModel
		}
		return githubmodels.NewGitHubModelsClient(tp, model, m.logger, maxRetries, initialBackoff), nil
	}
}

// configurarOpenRouterClient configura o cliente OpenRouter
func (m *LLMManagerImpl) configurarOpenRouterClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("OPENROUTER_API_KEY")
	if apiKey != "" {
		m.logger.Info(i18n.T("llm.info.configuring_provider", "OpenRouter"))
		m.clients["OPENROUTER"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultOpenRouterModel
			}
			provider := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
			return openrouter.NewOpenRouterClient(
				provider,
				model,
				m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}
	} else {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "OPENROUTER_API_KEY", "OPENROUTER"))
	}
}

// configurarDevinCLIClient registra o provider DEVIN, um wrapper do Devin CLI
// local (Cognition). Registro é gateado na presença do binário: sem ele o
// provider simplesmente não aparece, igual aos providers sem credencial.
// A autenticação é do próprio CLI (devin auth login / SSO da empresa) e é
// validada em runtime — erro de auth vira mensagem acionável no turno.
func (m *LLMManagerImpl) configurarDevinCLIClient(maxRetries int, initialBackoff time.Duration) {
	binPath, err := devincli.ResolveBinary()
	if err != nil {
		// Warn, não Info: em servidores ACP/MCP spawnados por IDE o PATH é o
		// mínimo da sessão GUI e este é o único diagnóstico do sumiço do
		// provider — precisa aparecer no app.log em nível padrão.
		m.logger.Warn(i18n.T("llm.devincli.not_available"), zap.Error(err))
		return
	}
	m.logger.Info(i18n.T("llm.info.configuring_provider", "Devin CLI"), zap.String("bin", binPath))
	m.clients["DEVIN"] = func(model string) (client.LLMClient, error) {
		if model == "" {
			model = config.Global.GetString("DEVIN_MODEL")
		}
		if model == "" {
			model = config.DefaultDevinModel
		}
		return devincli.NewClient(binPath, model, m.logger, maxRetries, initialBackoff), nil
	}
}

// GetAvailableProviders retorna uma lista de provedores disponíveis configurados
func (m *LLMManagerImpl) GetAvailableProviders() []string {
	providers := make([]string, 0, len(m.clients))
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
		m.logger.Warn(i18n.T("llm.manager.client_attempt_failed"),
			zap.String("provider", provider))
		return nil, fmt.Errorf("%s", i18n.T("llm.manager.provider_unsupported", provider))
	}

	clientInstance, err := factoryFunc(model)
	if err != nil {
		m.logger.Error(i18n.T("llm.manager.client_create_error"),
			zap.String("provider", provider),
			zap.String("model", model),
			zap.Error(err))
		return nil, err
	}

	return clientInstance, nil
}

// ListModelsForProvider lists available models for a provider. If the provider's
// client implements client.ModelLister, it fetches models dynamically from the API.
// Otherwise, it falls back to the static catalog.
func (m *LLMManagerImpl) ListModelsForProvider(ctx context.Context, provider string) ([]client.ModelInfo, error) {
	factoryFunc, ok := m.clients[provider]
	if !ok {
		return nil, fmt.Errorf("%s", i18n.T("llm.manager.provider_unsupported", provider))
	}

	// Create a temporary client to check if it supports model listing
	tempClient, err := factoryFunc("")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_create_for_listing"), err)
	}

	// Collect models from API (if supported) + catalog, deduplicated.
	seen := make(map[string]bool)
	var result []client.ModelInfo

	// Try API listing first
	if lister, ok := tempClient.(client.ModelLister); ok {
		m.logger.Debug("Attempting API model listing",
			zap.String("provider", provider),
			zap.String("clientType", fmt.Sprintf("%T", tempClient)))
		apiModels, err := lister.ListModels(ctx)
		if err != nil {
			m.logger.Warn(i18n.T("llm.manager.api_listing_failed"),
				zap.String("provider", provider), zap.Error(err))
		} else {
			m.logger.Debug("API model listing succeeded",
				zap.String("provider", provider),
				zap.Int("count", len(apiModels)))
			for _, am := range apiModels {
				if !seen[am.ID] {
					seen[am.ID] = true
					result = append(result, am)
				}
			}
		}
	} else {
		m.logger.Debug("Client does not implement ModelLister",
			zap.String("provider", provider),
			zap.String("clientType", fmt.Sprintf("%T", tempClient)))
	}

	// Always merge catalog models (they may include models the API doesn't list)
	metas := catalog.ListByProvider(provider)
	for _, meta := range metas {
		if !seen[meta.ID] {
			seen[meta.ID] = true
			result = append(result, client.ModelInfo{
				ID:          meta.ID,
				DisplayName: meta.DisplayName,
				Source:      client.ModelSourceCatalog,
			})
		}
	}

	return result, nil
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

// RefreshProviders re-checks auth credentials and registers/updates providers.
// Called after an OAuth login or token refresh at runtime, and after env
// reloads that may change provider availability.
// Closes the cached TokenProviders so the next call resolves from the fresh
// store contents and starts a new background refresh goroutine.
func (m *LLMManagerImpl) RefreshProviders() {
	maxRetries := config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries)
	initialBackoff := config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)

	m.closeTokenProviders()
	auth.InvalidateCache()

	// Re-configure OAuth providers. configurar* only registers the factory
	// if the TokenProvider resolves, so an existing working factory is
	// preserved when the new resolve fails (e.g. refresh token also expired).
	m.configurarOpenAIClient(maxRetries, initialBackoff)
	m.configurarClaudeAIClient(maxRetries, initialBackoff)
	m.configurarCopilotClient(maxRetries, initialBackoff)
	m.configurarGitHubModelsClient(maxRetries, initialBackoff)
	m.configurarZAIClient(maxRetries, initialBackoff)
	m.configurarMiniMaxClient(maxRetries, initialBackoff)
	m.configurarMoonshotClient(maxRetries, initialBackoff)
	m.configurarOpenRouterClient(maxRetries, initialBackoff)

	// Binary-gated provider: re-probe so a runtime env fix (DEVIN_CLI_PATH is
	// reloadable) surfaces DEVIN without a restart. Like the OAuth set above,
	// a failed probe preserves an existing registration.
	m.configurarDevinCLIClient(maxRetries, initialBackoff)
}

// CreateClientWithKey creates an LLM client using a caller-provided API key
// instead of the server's default credentials. The key is wrapped in a
// non-refreshing TokenProvider — refresh-on-401 is handled by the originating
// (remote) client, not the server forwarding the credential.
func (m *LLMManagerImpl) CreateClientWithKey(provider, model, apiKey string) (client.LLMClient, error) {
	maxRetries := config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries)
	initialBackoff := config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)

	provider = strings.ToUpper(provider)

	switch provider {
	case "OPENAI":
		if model == "" {
			model = config.DefaultOpenAIModel
		}
		tp := auth.NewStaticTokenProviderFromResolved(apiKey, auth.ProviderOpenAI)
		isOAuth := tp.Mode() == auth.AuthModeOAuth
		useResponses := isOAuth || config.Global.GetBool("OPENAI_USE_RESPONSES", false)
		if !useResponses && catalog.GetPreferredAPI(catalog.ProviderOpenAI, model) == catalog.APIResponses {
			useResponses = true
		}
		if useResponses {
			return openairesponses.NewOpenAIResponsesClient(tp, model, m.logger, maxRetries, initialBackoff), nil
		}
		return openai.NewOpenAIClient(tp, model, m.logger, maxRetries, initialBackoff), nil

	case "CLAUDEAI":
		if model == "" {
			model = config.DefaultClaudeAIModel
		}
		tp := auth.NewStaticTokenProviderFromResolved(apiKey, auth.ProviderAnthropic)
		return claudeai.NewClaudeClient(tp, model, m.logger, maxRetries, initialBackoff), nil

	case "GOOGLEAI":
		if model == "" {
			model = config.DefaultGoogleAIModel
		}
		tp := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
		return googleai.NewGeminiClient(tp, model, m.logger, maxRetries, initialBackoff), nil

	case "XAI":
		if model == "" {
			model = config.DefaultXAIModel
		}
		tp := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
		return xai.NewXAIClient(tp, model, m.logger, maxRetries, initialBackoff), nil

	case "ZAI":
		if model == "" {
			model = config.DefaultZAIModel
		}
		tp := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
		return zai.NewZAIClient(m.baseCtx, tp, model, m.logger, maxRetries, initialBackoff), nil

	case "MINIMAX":
		if model == "" {
			model = config.DefaultMiniMaxModel
		}
		tp := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
		return minimax.NewMiniMaxClient(tp, model, m.logger, maxRetries, initialBackoff), nil

	case "MOONSHOT":
		if model == "" {
			model = config.DefaultMoonshotModel
		}
		tp := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
		return moonshot.NewMoonshotClient(tp, model, m.logger, maxRetries, initialBackoff), nil

	case "COPILOT":
		if model == "" {
			model = config.DefaultCopilotModel
		}
		tp := auth.NewStaticTokenProviderFromResolved(apiKey, auth.ProviderGitHubCopilot)
		return copilot.NewClient(tp, model, m.logger, maxRetries, initialBackoff), nil

	case "OPENROUTER":
		if model == "" {
			// OPENROUTER_MODEL is documented on the site's configure-provider
			// guide but was never read anywhere — users following the docs
			// silently got the default model.
			model = resolveModelEnv("OPENROUTER_MODEL", config.DefaultOpenRouterModel)
		}
		tp := auth.NewStaticTokenProvider(apiKey, auth.AuthModeAPIKey, "")
		return openrouter.NewOpenRouterClient(tp, model, m.logger, maxRetries, initialBackoff), nil

	case "BEDROCK":
		if model == "" {
			model = resolveBedrockModel()
		}
		region, _ := bedrock.ResolveRegion()
		if region == "" {
			region = config.DefaultBedrockRegion
		}
		profile, _ := bedrock.ResolveProfile()
		return bedrock.NewBedrockClient(model, region, profile, m.logger, maxRetries, initialBackoff), nil

	default:
		return nil, fmt.Errorf("%s", i18n.T("llm.manager.create_client_unsupported", provider))
	}
}

// CreateClientWithConfig creates an LLM client using caller-provided credentials
// plus provider-specific configuration from the providerConfig map.
// Supports all providers including StackSpot and Ollama.
//
// StackSpot config keys: "client_id", "client_key", "realm", "agent_id"
// Ollama config keys: "base_url"
func (m *LLMManagerImpl) CreateClientWithConfig(provider, model, apiKey string, providerConfig map[string]string) (client.LLMClient, error) {
	maxRetries := config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries)
	initialBackoff := config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)

	provider = strings.ToUpper(provider)

	switch provider {
	case "STACKSPOT":
		clientID := providerConfig["client_id"]
		clientKey := providerConfig["client_key"]
		realm := providerConfig["realm"]
		agentID := providerConfig["agent_id"]

		if clientID == "" || clientKey == "" {
			return nil, fmt.Errorf("%s", i18n.T("llm.manager.stackspot_requires_field", "client_id and client_key"))
		}
		if realm == "" {
			return nil, fmt.Errorf("%s", i18n.T("llm.manager.stackspot_requires_field", "realm"))
		}
		if agentID == "" {
			return nil, fmt.Errorf("%s", i18n.T("llm.manager.stackspot_requires_field", "agent_id"))
		}

		tm := token.NewTokenManager(clientID, clientKey, realm, m.logger)
		return stackspotai.NewStackSpotClient(tm, agentID, m.logger, maxRetries, initialBackoff), nil

	case "OLLAMA":
		baseURL := providerConfig["base_url"]
		if baseURL == "" {
			baseURL = config.OllamaDefaultBaseURL
		}
		if model == "" {
			model = config.DefaultOllamaModel
		}
		return ollama.NewClient(baseURL, model, m.logger, maxRetries, initialBackoff), nil

	case "DEVIN":
		// Sem credencial própria: a autenticação é do binário devin local
		// (devin auth login). Só o binário precisa existir na máquina.
		binPath, err := devincli.ResolveBinary()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.devincli.not_available"), err)
		}
		if model == "" {
			model = config.DefaultDevinModel
		}
		return devincli.NewClient(binPath, model, m.logger, maxRetries, initialBackoff), nil

	default:
		// For simple API-key providers, delegate to CreateClientWithKey
		return m.CreateClientWithKey(provider, model, apiKey)
	}
}
