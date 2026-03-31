package catalog

import (
	"strings"
	"sync"

	"github.com/diillson/chatcli/config"
)

// mu protects the registry for concurrent read/write access.
var mu sync.RWMutex

// Provider names (alinhado com o restante do projeto)
const (
	ProviderOpenAI          = "OPENAI"
	ProviderOpenAIAssistant = "OPENAI_ASSISTANT"
	ProviderClaudeAI        = "CLAUDEAI"
	ProviderStackSpot       = "STACKSPOT"
	ProviderGoogleAI        = "GOOGLEAI"
	ProviderXAI             = "XAI"
	ProviderOllama          = "OLLAMA"
	ProviderCopilot         = "COPILOT"
	ProviderGitHubModels    = "GITHUB_MODELS"
)

// PreferredAPI define qual API é preferida para o modelo
// - "chat_completions": OpenAI Chat Completions
// - "responses": OpenAI Responses API
// - "assistants": OpenAI Assistants API
// - "anthropic_messages": Anthropic Messages API
type PreferredAPI string

const (
	APIChatCompletions   PreferredAPI = "chat_completions"
	APIResponses         PreferredAPI = "responses"
	APIAssistants        PreferredAPI = "assistants"
	APIAnthropicMessages PreferredAPI = "anthropic_messages"
)

// ModelMeta guarda metadados estáticos e seguros
type ModelMeta struct {
	ID              string       // ID oficial ou base
	Aliases         []string     // apelidos/variações aceitas (prefixos, variantes com datas)
	DisplayName     string       // Nome legível para UI
	Provider        string       // OPENAI, CLAUDEAI, etc.
	ContextWindow   int          // tokens de contexto (se conhecido; usar valor conservador caso contrário)
	MaxOutputTokens int          // limite recomendado de output (para chunking/limites)
	PreferredAPI    PreferredAPI // API preferida
	APIVersion      string       // versão de API (Anthropic), se aplicável
	Capabilities    []string     // ex.: ["tools","vision","json_mode"]
}

// registry: lista plana para facilitar matching por provedor + id/alias
var registry = []ModelMeta{
	// OpenAI GPT-5 e variantes
	{
		ID:              "gpt-5",
		Aliases:         []string{"gpt-5", "gpt-5.1", "gpt-5-mini", "gpt-5-nano"},
		DisplayName:     "GPT-5",
		Provider:        ProviderOpenAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		//PreferredAPI:    APIChatCompletions, // Possivel Roolback para APIResponses
		PreferredAPI: APIResponses,
		Capabilities: []string{"json_mode", "tools"},
	},
	{
		ID:              "gpt-5.2",
		Aliases:         []string{"gpt-5.2", "gpt-5.2-mini", "gpt-5.2-nano"},
		DisplayName:     "GPT-5.2",
		Provider:        ProviderOpenAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		//PreferredAPI:    APIChatCompletions, // Possivel Roolback para APIResponses
		PreferredAPI: APIResponses,
		Capabilities: []string{"json_mode", "tools"},
	},
	{
		ID:              "gpt-5.3-codex",
		Aliases:         []string{"gpt-5.3", "gpt-5.3-mini", "gpt-5.3-nano"},
		DisplayName:     "GPT-5.3",
		Provider:        ProviderOpenAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		//PreferredAPI:    APIChatCompletions, // Possivel Roolback para APIResponses
		PreferredAPI: APIResponses,
		Capabilities: []string{"json_mode", "tools"},
	},
	{
		ID:              "gpt-5.4",
		Aliases:         []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"},
		DisplayName:     "GPT-5.4",
		Provider:        ProviderOpenAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		//PreferredAPI:    APIChatCompletions, // Possivel Roolback para APIResponses
		PreferredAPI: APIResponses,
		Capabilities: []string{"json_mode", "tools"},
	},
	// OpenAI GPT-4o e variantes já suportadas
	{
		ID:              "gpt-4o",
		Aliases:         []string{"gpt-4o"},
		DisplayName:     "GPT-4o",
		Provider:        ProviderOpenAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-4o-mini",
		Aliases:         []string{"gpt-4o-mini"},
		DisplayName:     "GPT-4o mini",
		Provider:        ProviderOpenAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-4",
		Aliases:         []string{"gpt-4", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano"},
		DisplayName:     "GPT-4 family",
		Provider:        ProviderOpenAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	// Claude 4 e 4.1 (sonnet/opus)
	{
		ID:              "claude-sonnet-4",
		Aliases:         []string{"claude-4-sonnet", "sonnet-4-20250514", "claude-4-sonnet-"},
		DisplayName:     "Claude sonnet 4",
		Provider:        ProviderClaudeAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "claude-sonnet-4-5",
		Aliases:         []string{"claude-4-5-sonnet", "sonnet-4-5", "claude-4-5-sonnet-", "claude-sonnet-4-5-"},
		DisplayName:     "Claude sonnet 4.5",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "claude-sonnet-4-6",
		Aliases:         []string{"claude-4-6-sonnet", "sonnet-4-6", "claude-4-6-sonnet-", "claude-sonnet-4-6-"},
		DisplayName:     "Claude sonnet 4.6",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "claude-opus-4-20250514",
		Aliases:         []string{"opus-4", "claude-opus-4-20250514"},
		DisplayName:     "Claude opus 4",
		Provider:        ProviderClaudeAI,
		ContextWindow:   20000,
		MaxOutputTokens: 20000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "claude-opus-4-1-20250805",
		Aliases:         []string{"claude-opus-4-1", "opus-4-1", "claude-opus-4-1-20250805"},
		DisplayName:     "Claude opus 4.1",
		Provider:        ProviderClaudeAI,
		ContextWindow:   20000,
		MaxOutputTokens: 20000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "claude-opus-4-5",
		Aliases:         []string{"claude-opus-4-5", "opus-4-5"},
		DisplayName:     "Claude opus 4.5",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "claude-opus-4-6",
		Aliases:         []string{"claude-opus-4-6", "opus-4-6"},
		DisplayName:     "Claude opus 4.6",
		Provider:        ProviderClaudeAI,
		ContextWindow:   400000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	// Claude 3.x já existentes no projeto
	{
		ID:              "claude-sonnet-3-5-20241022",
		Aliases:         []string{"claude-3-5-sonnet", "claude-3-5-sonnet-20241022"},
		DisplayName:     "Claude sonnet 3.5",
		Provider:        ProviderClaudeAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "claude-sonnet-3-7-20250219",
		Aliases:         []string{"claude-3-7-sonnet", "claude-3-7-sonnet-20250219"},
		DisplayName:     "Claude sonnet 3.7",
		Provider:        ProviderClaudeAI,
		ContextWindow:   50000,
		MaxOutputTokens: 50000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "claude-haiku-3",
		Aliases:         []string{"claude-3-haiku"},
		DisplayName:     "Claude haiku 3",
		Provider:        ProviderClaudeAI,
		ContextWindow:   42000,
		MaxOutputTokens: 42000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode"},
	},
	{
		ID:              "claude-opus-3",
		Aliases:         []string{"claude-3-opus"},
		DisplayName:     "claude opus 3",
		Provider:        ProviderClaudeAI,
		ContextWindow:   32000,
		MaxOutputTokens: 32000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	// Google Gemini Models
	{
		ID:              "gemini-2.5-pro",
		Aliases:         []string{"gemini-2.5-pro", "gemini-2.5-pro-latest"},
		DisplayName:     "Gemini 2.5 Pro",
		Provider:        ProviderGoogleAI,
		ContextWindow:   2000000, // 2M tokens context window
		MaxOutputTokens: 2000000,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "code_execution"},
	},
	{
		ID:              "gemini-3",
		Aliases:         []string{"gemini-3-pro", "gemini-3-pro-preview"},
		DisplayName:     "Gemini 3 Pro",
		Provider:        ProviderGoogleAI,
		ContextWindow:   2000000, // 2M tokens context window
		MaxOutputTokens: 2000000,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "code_execution"},
	},
	{
		ID:              "gemini-2.5-flash",
		Aliases:         []string{"gemini-2.5-flash"},
		DisplayName:     "Gemini 2.5 Flash",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1000000, // 1M tokens
		MaxOutputTokens: 1000000,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gemini-2.5-flash-lite",
		Aliases:         []string{"gemini-2.5-flash-lite", "gemini-2.5-flash-lite"},
		DisplayName:     "Gemini 2.5 Flash Lite",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 1000000,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "multimodal_live"},
	},
	{
		ID:              "gemini-2.0-flash",
		Aliases:         []string{"gemini-2.0-flash"},
		DisplayName:     "Gemini 2.0 Flash",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1000000, // 1M tokens
		MaxOutputTokens: 1000000,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gemini-2.0-flash-lite",
		Aliases:         []string{"gemini-2.0-flash-lite"},
		DisplayName:     "Gemini 2.5 Flash Lite",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 1000000,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "multimodal_live"},
	},
	// GitHub Copilot Models (accessible via Copilot subscription)
	{
		ID:              "gpt-4o",
		Aliases:         []string{"copilot-gpt-4o"},
		DisplayName:     "GPT-4o (Copilot)",
		Provider:        ProviderCopilot,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-4o-mini",
		Aliases:         []string{"copilot-gpt-4o-mini"},
		DisplayName:     "GPT-4o mini (Copilot)",
		Provider:        ProviderCopilot,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "claude-sonnet-4",
		Aliases:         []string{"copilot-claude-sonnet-4"},
		DisplayName:     "Claude Sonnet 4 (Copilot)",
		Provider:        ProviderCopilot,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
	},
	{
		ID:              "gemini-2.0-flash",
		Aliases:         []string{"copilot-gemini-2.0-flash"},
		DisplayName:     "Gemini 2.0 Flash (Copilot)",
		Provider:        ProviderCopilot,
		ContextWindow:   1000000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
	},
	// GitHub Models marketplace (models.inference.ai.azure.com)
	// These are the known models available via GitHub PAT.
	// The actual availability depends on the user's GitHub plan.
	{
		ID:              "gpt-4o",
		Aliases:         []string{"gh-gpt-4o"},
		DisplayName:     "GPT-4o (GitHub Models)",
		Provider:        ProviderGitHubModels,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-4o-mini",
		Aliases:         []string{"gh-gpt-4o-mini"},
		DisplayName:     "GPT-4o mini (GitHub Models)",
		Provider:        ProviderGitHubModels,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "Meta-Llama-3.1-405B-Instruct",
		Aliases:         []string{"llama-3.1-405b", "meta-llama-405b"},
		DisplayName:     "Llama 3.1 405B (GitHub Models)",
		Provider:        ProviderGitHubModels,
		ContextWindow:   128000,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIChatCompletions,
	},
	{
		ID:              "Meta-Llama-3.1-8B-Instruct",
		Aliases:         []string{"llama-3.1-8b", "meta-llama-8b"},
		DisplayName:     "Llama 3.1 8B (GitHub Models)",
		Provider:        ProviderGitHubModels,
		ContextWindow:   128000,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIChatCompletions,
	},
	// Models below require GitHub Copilot Pro or expanded access
	{
		ID:              "DeepSeek-R1",
		Aliases:         []string{"deepseek-r1", "deepseek"},
		DisplayName:     "DeepSeek R1 (GitHub Models)",
		Provider:        ProviderGitHubModels,
		ContextWindow:   64000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIChatCompletions,
	},
	{
		ID:              "Mistral-large-2411",
		Aliases:         []string{"mistral-large", "mistral"},
		DisplayName:     "Mistral Large (GitHub Models)",
		Provider:        ProviderGitHubModels,
		ContextWindow:   128000,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIChatCompletions,
	},
	{
		ID:              "Phi-4",
		Aliases:         []string{"phi-4", "phi4"},
		DisplayName:     "Phi-4 (GitHub Models)",
		Provider:        ProviderGitHubModels,
		ContextWindow:   16384,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIChatCompletions,
	},
	{
		ID:              "AI21-Jamba-1.5-Large",
		Aliases:         []string{"jamba-1.5-large", "jamba"},
		DisplayName:     "Jamba 1.5 Large (GitHub Models)",
		Provider:        ProviderGitHubModels,
		ContextWindow:   256000,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIChatCompletions,
	},
	{
		ID:              "Cohere-command-r-plus-08-2024",
		Aliases:         []string{"cohere-command-r-plus", "cohere"},
		DisplayName:     "Cohere Command R+ (GitHub Models)",
		Provider:        ProviderGitHubModels,
		ContextWindow:   128000,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIChatCompletions,
	},

	// xAI (Grok) Models
	{
		ID:              "grok-4-fast",
		Aliases:         []string{"grok-4-fast-reasoning-latest, grok-4-fast-reasoning, grok-4-0709"},
		DisplayName:     "Grok-4",
		Provider:        ProviderXAI,
		ContextWindow:   2000000,
		MaxOutputTokens: 2000000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{},
	},
	{
		ID:              "grok-4-1",
		Aliases:         []string{"grok-4-1-fast, grok-4-1-fast-reasoning-latest"},
		DisplayName:     "Grok-4-1",
		Provider:        ProviderXAI,
		ContextWindow:   2000000,
		MaxOutputTokens: 2000000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{},
	},
	{
		ID:              "grok-3",
		Aliases:         []string{"grok-3"},
		DisplayName:     "Grok-3",
		Provider:        ProviderXAI,
		ContextWindow:   128000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"json_mode"},
	},
	{
		ID:              "grok-3-mini",
		Aliases:         []string{"grok-3-mini"},
		DisplayName:     "Grok-3 Mini",
		Provider:        ProviderXAI,
		ContextWindow:   128000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"json_mode"},
	},
	{
		ID:              "grok-code-fast-1",
		Aliases:         []string{"grok-code-fast-1"},
		DisplayName:     "Grok Code Fast 1",
		Provider:        ProviderXAI,
		ContextWindow:   200000,
		MaxOutputTokens: 200000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"json_mode"},
	},
}

// Resolve procura metadados por provedor e string de modelo (case-insensitive),
// aceitando match exato, por prefixo ou por aliases.
func Resolve(provider, model string) (ModelMeta, bool) {
	if model == "" {
		return ModelMeta{}, false
	}
	mu.RLock()
	defer mu.RUnlock()
	m := strings.ToLower(model)
	p := strings.ToUpper(provider)

	// 1) match por provedor + id exato (normalizado)
	for _, meta := range registry {
		if meta.Provider != "" && meta.Provider != p {
			continue
		}
		if strings.EqualFold(meta.ID, model) {
			return meta, true
		}
	}

	// 2) match por provedor + aliases (contém/prefixo)
	for _, meta := range registry {
		if meta.Provider != "" && meta.Provider != p {
			continue
		}
		for _, alias := range meta.Aliases {
			a := strings.ToLower(alias)
			if m == a || strings.HasPrefix(m, a) || strings.Contains(m, a) {
				return meta, true
			}
		}
	}

	// 3) match por provedor + prefixo do ID
	for _, meta := range registry {
		if meta.Provider != "" && meta.Provider != p {
			continue
		}
		if strings.HasPrefix(m, strings.ToLower(meta.ID)) {
			return meta, true
		}
	}

	return ModelMeta{}, false
}

// GetMaxTokens sugere maxTokens com prioridade:
// 1) override > 0
// 2) registry.MaxOutputTokens (se disponível)
// 3) fallback conservador por provedor/modelo
func GetMaxTokens(provider, model string, override int) int {
	if override > 0 {
		return override
	}
	if meta, ok := Resolve(provider, model); ok && meta.MaxOutputTokens > 0 {
		return meta.MaxOutputTokens
	}

	// fallbacks conservadores, coerentes com o código existente
	switch strings.ToUpper(provider) {
	case ProviderOpenAI:
		m := strings.ToLower(model)
		if strings.HasPrefix(m, "gpt-5") {
			return 50000
		}
		if m == "gpt-4o" || m == "gpt-4o-mini" || strings.HasPrefix(m, "gpt-4") {
			return 50000
		}
		return 40000
	case ProviderClaudeAI:
		return 50000
	case ProviderStackSpot:
		return 50000
	case ProviderGoogleAI:
		return 50000
	case ProviderXAI:
		return 50000
	case ProviderOllama:
		return 8192
	case ProviderCopilot:
		return 16384
	case ProviderGitHubModels:
		return 4096
	default:
		return 50000
	}
}

// GetContextWindow returns the context window size (in tokens) for the given
// provider+model. Falls back to a conservative default if not found.
func GetContextWindow(provider, model string) int {
	if meta, ok := Resolve(provider, model); ok && meta.ContextWindow > 0 {
		return meta.ContextWindow
	}
	switch strings.ToUpper(provider) {
	case ProviderGoogleAI:
		return 1000000
	case ProviderClaudeAI:
		return 200000
	case ProviderOpenAI:
		return 128000
	case ProviderXAI:
		return 128000
	case ProviderOllama:
		return 8192
	case ProviderCopilot:
		return 128000
	case ProviderGitHubModels:
		return 128000
	default:
		return 50000
	}
}

// GetAnthropicAPIVersion retorna a versão da API para Anthropic (Claude),
// priorizando meta.APIVersion; se não houver, retorna o default configurado.
func GetAnthropicAPIVersion(model string) string {
	if meta, ok := Resolve(ProviderClaudeAI, model); ok && meta.APIVersion != "" {
		return meta.APIVersion
	}
	return config.ClaudeAIAPIVersionDefault
}

// GetDisplayName tenta retornar um nome amigável a partir do registry.
// Se não houver match, retorna o próprio model ID.
func GetDisplayName(provider, model string) string {
	if meta, ok := Resolve(provider, model); ok && meta.DisplayName != "" {
		return meta.DisplayName
	}
	return model
}

// GetPreferredAPI expõe a API preferida (para uso futuro na Parte 3).
func GetPreferredAPI(provider, model string) PreferredAPI {
	if meta, ok := Resolve(provider, model); ok && meta.PreferredAPI != "" {
		return meta.PreferredAPI
	}
	switch strings.ToUpper(provider) {
	case ProviderOpenAI:
		return APIChatCompletions
	case ProviderOpenAIAssistant:
		return APIAssistants
	case ProviderClaudeAI:
		return APIAnthropicMessages
	case ProviderGoogleAI:
		return PreferredAPI("gemini_api")
	default:
		return APIChatCompletions
	}
}

// HasCapability verifica se o modelo anuncia determinada capacidade (best-effort).
func HasCapability(provider, model, capability string) bool {
	if meta, ok := Resolve(provider, model); ok {
		cap := strings.ToLower(capability)
		for _, c := range meta.Capabilities {
			if strings.ToLower(c) == cap {
				return true
			}
		}
	}
	return false
}

// Lista (best-effort) de todos ModelMeta cadastrados (pode ser útil para debug).
func ListAll() []ModelMeta {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]ModelMeta, len(registry))
	copy(out, registry)
	return out
}

// Register adds a ModelMeta to the registry. If a model with the same
// Provider+ID already exists, it is replaced (to support dynamic refresh).
func Register(meta ModelMeta) {
	mu.Lock()
	defer mu.Unlock()
	p := strings.ToUpper(meta.Provider)
	id := strings.ToLower(meta.ID)
	for i, existing := range registry {
		if strings.ToUpper(existing.Provider) == p && strings.ToLower(existing.ID) == id {
			registry[i] = meta
			return
		}
	}
	registry = append(registry, meta)
}

// ListByProvider returns all ModelMeta entries for a given provider.
func ListByProvider(provider string) []ModelMeta {
	mu.RLock()
	defer mu.RUnlock()
	p := strings.ToUpper(provider)
	var out []ModelMeta
	for _, meta := range registry {
		if strings.ToUpper(meta.Provider) == p {
			out = append(out, meta)
		}
	}
	return out
}
