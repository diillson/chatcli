package catalog

import (
	"strings"

	"github.com/diillson/chatcli/config"
)

// Provider names (alinhado com o restante do projeto)
const (
	ProviderOpenAI          = "OPENAI"
	ProviderOpenAIAssistant = "OPENAI_ASSISTANT"
	ProviderClaudeAI        = "CLAUDEAI"
	ProviderStackSpot       = "STACKSPOT"
	ProviderGoogleAI        = "GOOGLEAI"
	ProviderXAI             = "XAI"
	ProviderOllama          = "OLLAMA"
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
		Aliases:         []string{"gpt-5", "gpt-5.2", "gpt-5-mini", "gpt-5-nano"},
		DisplayName:     "GPT-5",
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
	out := make([]ModelMeta, len(registry))
	copy(out, registry)
	return out
}
