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
	github_models "github.com/diillson/chatcli/llm/github_models"
	"github.com/diillson/chatcli/llm/googleai"
	"github.com/diillson/chatcli/llm/minimax"
	"github.com/diillson/chatcli/llm/ollama"
	"github.com/diillson/chatcli/llm/openai"
	"github.com/diillson/chatcli/llm/openai_assistant"
	"github.com/diillson/chatcli/llm/openai_responses"
	"github.com/diillson/chatcli/llm/openrouter"
	"github.com/diillson/chatcli/llm/stackspotai"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/llm/xai"
	"github.com/diillson/chatcli/llm/zai"
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
}

// NewLLMManager cria uma nova instância de LLMManagerImpl.
func NewLLMManager(logger *zap.Logger) (LLMManager, error) {
	maxRetries := config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries)
	initialBackoff := config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)

	logger.Info(i18n.T("llm.manager.retry_policy"),
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
	manager.configurarZAIClient(maxRetries, initialBackoff)
	manager.configurarMiniMaxClient(maxRetries, initialBackoff)
	manager.configurarOllamaClient(maxRetries, initialBackoff)
	manager.configurarCopilotClient(maxRetries, initialBackoff)
	manager.configurarGitHubModelsClient(maxRetries, initialBackoff)
	manager.configurarOpenRouterClient(maxRetries, initialBackoff)
	manager.configurarBedrockClient(maxRetries, initialBackoff)

	return manager, nil
}

// configurarBedrockClient registra o provedor AWS Bedrock quando há sinal de
// configuração AWS (env vars, AWS_PROFILE, ou ~/.aws/credentials). A resolução
// real das credenciais acontece apenas na primeira chamada (credentials chain
// padrão: env → shared config → IAM role).
func (m *LLMManagerImpl) configurarBedrockClient(maxRetries int, initialBackoff time.Duration) {
	if !bedrockCredentialsAvailable() {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "AWS credentials", "BEDROCK"))
		return
	}
	m.logger.Info(i18n.T("llm.info.configuring_provider", "AWS Bedrock"))
	m.clients["BEDROCK"] = func(model string) (client.LLMClient, error) {
		if model == "" {
			model = config.DefaultBedrockModel
		}
		region := firstNonEmptyEnv("BEDROCK_REGION", "AWS_REGION")
		if region == "" {
			region = config.DefaultBedrockRegion
		}
		profile := resolveAWSProfile()
		return bedrock.NewBedrockClient(model, region, profile, m.logger, maxRetries, initialBackoff), nil
	}
}

// bedrockCredentialsAvailable reports whether there is a credible signal that
// AWS credentials are usable. It intentionally does NOT treat the mere existence
// of ~/.aws/config as a credential (that file may hold only region/profile
// metadata), nor does it treat an empty ~/.aws/credentials as valid — otherwise
// the AWS SDK falls through to IMDS (169.254.169.254) and hangs with a
// confusing timeout on non-EC2 machines.
//
// Accepted signals (any one is enough):
//   - Static creds via env (AWS_ACCESS_KEY_ID)
//   - Profile selection (AWS_PROFILE) — SSO, assume-role, credential_process, etc.
//   - Web identity / container roles (EKS / ECS)
//   - ~/.aws/credentials with a non-empty aws_access_key_id
//   - ~/.aws/config declaring a usable profile: SSO (sso_start_url / sso_session),
//     assume-role (role_arn), or credential_process
//   - An active SSO token cache in ~/.aws/sso/cache/
func bedrockCredentialsAvailable() bool {
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" ||
		os.Getenv("AWS_PROFILE") != "" ||
		os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" ||
		os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
		os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" {
		return true
	}
	// Also check .env-sourced config (godotenv doesn't export to os.Environ).
	if config.Global != nil && config.Global.GetString("AWS_PROFILE") != "" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	if credentialsFileHasKey(home + "/.aws/credentials") {
		return true
	}
	if configFileHasUsableProfile(home + "/.aws/config") {
		return true
	}
	return hasActiveSSOCache(home + "/.aws/sso/cache")
}

// credentialsFileHasKey returns true only if the AWS shared credentials file
// contains at least one aws_access_key_id entry. An empty or region-only file
// is not enough to activate Bedrock.
func credentialsFileHasKey(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "aws_access_key_id") {
			if idx := strings.Index(trimmed, "="); idx >= 0 {
				if strings.TrimSpace(trimmed[idx+1:]) != "" {
					return true
				}
			}
		}
	}
	return false
}

// configFileHasUsableProfile returns true if ~/.aws/config declares at least
// one profile that can produce credentials without IMDS: SSO, assume-role, or
// credential_process. A config that only sets region/output does NOT count.
func configFileHasUsableProfile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	usableKeys := []string{
		"sso_start_url",    // legacy SSO profile
		"sso_session",      // new-style SSO (aws sso login)
		"sso_account_id",   // ditto
		"role_arn",         // assume-role profile (source_profile / web identity)
		"credential_process",
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		lower := strings.ToLower(trimmed)
		for _, key := range usableKeys {
			if strings.HasPrefix(lower, key) {
				if idx := strings.Index(trimmed, "="); idx >= 0 &&
					strings.TrimSpace(trimmed[idx+1:]) != "" {
					return true
				}
			}
		}
	}
	return false
}

// hasActiveSSOCache returns true if ~/.aws/sso/cache contains any JSON token
// file. A stale/expired token will still be caught later at SDK call time, but
// its presence means the user has at least run `aws sso login` at some point.
func hasActiveSSOCache(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			return true
		}
	}
	return false
}

// resolveAWSProfile returns the AWS profile name from the first available
// source: env var AWS_PROFILE > .env file (via config.Global).
func resolveAWSProfile() string {
	if v := os.Getenv("AWS_PROFILE"); v != "" {
		return v
	}
	if config.Global != nil {
		if v := config.Global.GetString("AWS_PROFILE"); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
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
			return googleai.NewGeminiClient(
				apiKey,
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
func (m *LLMManagerImpl) configurarOpenAIClient(maxRetries int, initialBackoff time.Duration) {
	resolved, err := auth.ResolveAuth(context.Background(), auth.ProviderOpenAI, m.logger)
	if err != nil {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "OPENAI_API_KEY", "OPENAI"), zap.Error(err))
		return
	}
	if resolved.APIKey == "" {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "OPENAI_API_KEY", "OPENAI"))
		return
	}
	m.clients["OPENAI"] = func(model string) (client.LLMClient, error) {
		// Re-resolve auth on each client creation to pick up refreshed tokens
		res, err := auth.ResolveAuth(context.Background(), auth.ProviderOpenAI, m.logger)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "OpenAI"), err)
		}
		apiKey := res.APIKey
		if model == "" {
			model = config.DefaultOpenAIModel
		}

		// OAuth tokens always use the Responses API (ChatGPT backend only speaks Responses format)
		isOAuth := strings.HasPrefix(apiKey, "oauth:")

		useResponses := isOAuth || config.Global.GetBool("OPENAI_USE_RESPONSES", false)

		if !useResponses && catalog.GetPreferredAPI(catalog.ProviderOpenAI, model) == catalog.APIResponses {
			useResponses = true
		}

		if useResponses {
			m.logger.Info(i18n.T("llm.manager.using_responses_api"), zap.String("model", model), zap.Bool("oauth", isOAuth))
			return openai_responses.NewOpenAIResponsesClient(
				apiKey, model, m.logger,
				maxRetries,
				initialBackoff,
			), nil
		}

		m.logger.Info(i18n.T("llm.manager.using_chat_completions"), zap.String("model", model))
		return openai.NewOpenAIClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil
	}

	m.clients["OPENAI_ASSISTANT"] = func(model string) (client.LLMClient, error) {
		res, err := auth.ResolveAuth(context.Background(), auth.ProviderOpenAI, m.logger)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "OpenAI"), err)
		}
		if model == "" {
			model = config.DefaultOpenAiAssistModel
		}
		return openai_assistant.NewOpenAIAssistantClient(res.APIKey, model, m.logger)
	}
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
	resolved, err := auth.ResolveAuth(context.Background(), auth.ProviderAnthropic, m.logger)
	if err != nil {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "ANTHROPIC_API_KEY", "CLAUDEAI"), zap.Error(err))
		return
	}
	if resolved.APIKey == "" {
		m.logger.Warn(i18n.T("llm.warn.provider_not_available", "ANTHROPIC_API_KEY", "ClaudeAI"))
		return
	}
	m.clients["CLAUDEAI"] = func(model string) (client.LLMClient, error) {
		// Re-resolve auth on each client creation to pick up refreshed tokens
		res, err := auth.ResolveAuth(context.Background(), auth.ProviderAnthropic, m.logger)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "Anthropic"), err)
		}
		if model == "" {
			model = config.DefaultClaudeAIModel
		}
		return claudeai.NewClaudeClient(
			res.APIKey,
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
			return xai.NewXAIClient(
				apiKey,
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
			return zai.NewZAIClient(
				apiKey,
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

func (m *LLMManagerImpl) configurarMiniMaxClient(maxRetries int, initialBackoff time.Duration) {
	apiKey := config.Global.GetString("MINIMAX_API_KEY")
	if apiKey != "" {
		m.logger.Info(i18n.T("llm.info.configuring_provider", "MiniMax"))
		m.clients["MINIMAX"] = func(model string) (client.LLMClient, error) {
			if model == "" {
				model = config.DefaultMiniMaxModel
			}
			return minimax.NewMiniMaxClient(
				apiKey,
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

// configurarCopilotClient configura o cliente GitHub Copilot
func (m *LLMManagerImpl) configurarCopilotClient(maxRetries int, initialBackoff time.Duration) {
	resolved, err := auth.ResolveAuth(context.Background(), auth.ProviderGitHubCopilot, m.logger)
	if err != nil {
		m.logger.Info(i18n.T("llm.warn.provider_not_configured", "GitHub Copilot", "COPILOT"), zap.Error(err))
		return
	}
	if resolved.APIKey == "" {
		return
	}
	m.logger.Info(i18n.T("llm.info.configuring_provider", "GitHub Copilot"))
	m.clients["COPILOT"] = func(model string) (client.LLMClient, error) {
		// Re-resolve auth on each client creation to pick up refreshed tokens
		res, err := auth.ResolveAuth(context.Background(), auth.ProviderGitHubCopilot, m.logger)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "GitHub Copilot"), err)
		}
		if model == "" {
			model = config.DefaultCopilotModel
		}
		return copilot.NewClient(res.APIKey, model, m.logger, maxRetries, initialBackoff), nil
	}
}

// configurarGitHubModelsClient configura o cliente GitHub Models marketplace
func (m *LLMManagerImpl) configurarGitHubModelsClient(maxRetries int, initialBackoff time.Duration) {
	resolved, err := auth.ResolveAuth(context.Background(), auth.ProviderGitHubModels, m.logger)
	if err != nil {
		m.logger.Info(i18n.T("llm.warn.provider_not_configured", "GitHub Models", "GITHUB_MODELS"), zap.Error(err))
		return
	}
	if resolved.APIKey == "" {
		return
	}
	m.logger.Info(i18n.T("llm.info.configuring_provider", "GitHub Models"))
	m.clients["GITHUB_MODELS"] = func(model string) (client.LLMClient, error) {
		res, err := auth.ResolveAuth(context.Background(), auth.ProviderGitHubModels, m.logger)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("llm.manager.failed_resolve_auth", "GitHub Models"), err)
		}
		if model == "" {
			model = config.DefaultGitHubModelsModel
		}
		return github_models.NewGitHubModelsClient(res.APIKey, model, m.logger, maxRetries, initialBackoff), nil
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
			return openrouter.NewOpenRouterClient(
				apiKey,
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
// Called after an OAuth login or token refresh at runtime.
// Safe to call multiple times — only overwrites a factory if new credentials are available.
func (m *LLMManagerImpl) RefreshProviders() {
	maxRetries := config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries)
	initialBackoff := config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)

	auth.InvalidateCache()

	// Re-configure OAuth providers. configurar* only registers the factory
	// if ResolveAuth succeeds, so an existing working factory is preserved
	// when the new resolve fails (e.g. refresh token also expired).
	m.configurarOpenAIClient(maxRetries, initialBackoff)
	m.configurarClaudeAIClient(maxRetries, initialBackoff)
	m.configurarCopilotClient(maxRetries, initialBackoff)
	m.configurarGitHubModelsClient(maxRetries, initialBackoff)
	m.configurarZAIClient(maxRetries, initialBackoff)
	m.configurarMiniMaxClient(maxRetries, initialBackoff)
	m.configurarOpenRouterClient(maxRetries, initialBackoff)
}

// CreateClientWithKey creates an LLM client using a caller-provided API key
// instead of the server's default credentials. Supports OPENAI, CLAUDEAI,
// GOOGLEAI, XAI, ZAI, MINIMAX, and COPILOT providers. Returns an error for unsupported providers.
func (m *LLMManagerImpl) CreateClientWithKey(provider, model, apiKey string) (client.LLMClient, error) {
	maxRetries := config.Global.GetInt("MAX_RETRIES", config.DefaultMaxRetries)
	initialBackoff := config.Global.GetDuration("INITIAL_BACKOFF", config.DefaultInitialBackoff)

	provider = strings.ToUpper(provider)

	switch provider {
	case "OPENAI":
		if model == "" {
			model = config.DefaultOpenAIModel
		}
		isOAuth := strings.HasPrefix(apiKey, "oauth:")
		useResponses := isOAuth || config.Global.GetBool("OPENAI_USE_RESPONSES", false)
		if !useResponses && catalog.GetPreferredAPI(catalog.ProviderOpenAI, model) == catalog.APIResponses {
			useResponses = true
		}
		if useResponses {
			return openai_responses.NewOpenAIResponsesClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil
		}
		return openai.NewOpenAIClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil

	case "CLAUDEAI":
		if model == "" {
			model = config.DefaultClaudeAIModel
		}
		return claudeai.NewClaudeClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil

	case "GOOGLEAI":
		if model == "" {
			model = config.DefaultGoogleAIModel
		}
		return googleai.NewGeminiClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil

	case "XAI":
		if model == "" {
			model = config.DefaultXAIModel
		}
		return xai.NewXAIClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil

	case "ZAI":
		if model == "" {
			model = config.DefaultZAIModel
		}
		return zai.NewZAIClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil

	case "MINIMAX":
		if model == "" {
			model = config.DefaultMiniMaxModel
		}
		return minimax.NewMiniMaxClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil

	case "COPILOT":
		if model == "" {
			model = config.DefaultCopilotModel
		}
		return copilot.NewClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil

	case "OPENROUTER":
		if model == "" {
			model = config.DefaultOpenRouterModel
		}
		return openrouter.NewOpenRouterClient(apiKey, model, m.logger, maxRetries, initialBackoff), nil

	case "BEDROCK":
		if model == "" {
			model = config.DefaultBedrockModel
		}
		region := firstNonEmptyEnv("BEDROCK_REGION", "AWS_REGION")
		if region == "" {
			region = config.DefaultBedrockRegion
		}
		return bedrock.NewBedrockClient(model, region, resolveAWSProfile(), m.logger, maxRetries, initialBackoff), nil

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

	default:
		// For simple API-key providers, delegate to CreateClientWithKey
		return m.CreateClientWithKey(provider, model, apiKey)
	}
}
