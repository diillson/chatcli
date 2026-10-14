package catalog

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/diillson/chatcli/config"
)

// ProviderDescriptor is the single source of truth for a provider's static
// metadata: every surface that needs to know "which providers exist" or
// "what is provider X's default model / max-tokens env / auth flow" reads it
// from here instead of keeping its own list. Built-in providers are declared
// in builtinProviders; providers contributed at runtime (external provider
// plugins) join through RegisterProvider and get the same treatment.
//
// Client construction is intentionally NOT part of the descriptor: it stays
// in llm/manager, which owns credential resolution.
type ProviderDescriptor struct {
	// Name is the canonical, upper-case provider id (LLM_PROVIDER value).
	Name string
	// DisplayName is the human-facing label.
	DisplayName string

	// ModelEnv is the env var that selects the provider's model;
	// ModelFallbackEnv is consulted when ModelEnv is unset. DefaultModel is
	// used when neither is set; empty lets the client choose its own.
	ModelEnv         string
	ModelFallbackEnv string
	DefaultModel     string

	// MaxTokensEnv forces the per-request max_tokens for the provider.
	MaxTokensEnv string

	// AuthProvider is the auth.ProviderID of the provider's OAuth/local
	// credential flow ("" when it has none). Kept as a plain string so this
	// package stays dependency-free.
	AuthProvider string

	// SecretEnv lists the env vars holding the provider's credentials; they
	// are always treated as secrets by the env redactor.
	SecretEnv []string

	// PreferredAPI is the API family used for models absent from the model
	// registry; "" means chat completions.
	PreferredAPI PreferredAPI

	// FallbackMaxTokens and FallbackContextWindow size models absent from
	// the model registry. Nil means the generic conservative default.
	FallbackMaxTokens     func(model string) int
	FallbackContextWindow func(model string) int

	builtin bool
}

// Builtin reports whether the provider ships with chatcli.
func (d ProviderDescriptor) Builtin() bool { return d.builtin }

// defaultFallbackTokens is the conservative size for an unknown provider
// or model with no provider-specific rule.
const defaultFallbackTokens = 50000

func (d ProviderDescriptor) maxTokensFor(model string) int {
	if d.FallbackMaxTokens == nil {
		return defaultFallbackTokens
	}
	return d.FallbackMaxTokens(model)
}

func (d ProviderDescriptor) contextWindowFor(model string) int {
	if d.FallbackContextWindow == nil {
		return defaultFallbackTokens
	}
	return d.FallbackContextWindow(model)
}

func (d ProviderDescriptor) preferredAPI() PreferredAPI {
	if d.PreferredAPI == "" {
		return APIChatCompletions
	}
	return d.PreferredAPI
}

// clone detaches the descriptor's slices so callers cannot mutate the
// registry through a returned copy.
func (d ProviderDescriptor) clone() ProviderDescriptor {
	d.SecretEnv = append([]string(nil), d.SecretEnv...)
	return d
}

func fixedTokens(n int) func(string) int {
	return func(string) int { return n }
}

// builtinProviders is ordered: callers that scan providers front-to-back
// (bare model-hint routing) let the earliest entry win, so CLAUDEAI precedes
// BEDROCK — shared claude-* ids resolve to the direct Anthropic API unless
// the hint qualifies BEDROCK explicitly.
var builtinProviders = []ProviderDescriptor{
	{
		Name:         ProviderOpenAI,
		DisplayName:  "OpenAI",
		ModelEnv:     "OPENAI_MODEL",
		DefaultModel: config.DefaultOpenAIModel,
		MaxTokensEnv: "OPENAI_MAX_TOKENS",
		AuthProvider: "openai",
		SecretEnv:    []string{"OPENAI_API_KEY"},
		PreferredAPI: APIChatCompletions,
		FallbackMaxTokens: func(model string) int {
			m := strings.ToLower(model)
			// gpt-5 family (5, 5.x, 5.5/pro): real cap is 128K.
			if strings.HasPrefix(m, "gpt-5") {
				return 128000
			}
			// gpt-4.1 / gpt-4o family: real cap 16K-32K, use 32K.
			if m == "gpt-4o" || m == "gpt-4o-mini" || strings.HasPrefix(m, "gpt-4") {
				return 32000
			}
			// o-series reasoning models cap at 100K.
			if strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") {
				return 100000
			}
			return 16384
		},
		FallbackContextWindow: fixedTokens(128000),
	},
	{
		Name:                  ProviderOpenAIAssistant,
		DisplayName:           "OpenAI Assistants",
		ModelEnv:              "OPENAI_ASSISTANT_MODEL",
		ModelFallbackEnv:      "OPENAI_MODEL",
		DefaultModel:          config.DefaultOpenAiAssistModel,
		SecretEnv:             []string{"OPENAI_API_KEY"},
		PreferredAPI:          APIAssistants,
		FallbackContextWindow: fixedTokens(128000),
	},
	{
		Name:         ProviderClaudeAI,
		DisplayName:  "Claude (Anthropic)",
		ModelEnv:     "ANTHROPIC_MODEL",
		DefaultModel: config.DefaultClaudeAIModel,
		MaxTokensEnv: "ANTHROPIC_MAX_TOKENS",
		AuthProvider: "anthropic",
		SecretEnv:    []string{"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN"},
		PreferredAPI: APIAnthropicMessages,
		// Claude 3+ família toda usa 200K input; output varia 4K-128K.
		// 64K é o teto comum dos modelos atuais (sonnet 4.x, opus 4.5+).
		FallbackMaxTokens:     fixedTokens(64000),
		FallbackContextWindow: fixedTokens(200000),
	},
	{
		Name:         ProviderBedrock,
		DisplayName:  "AWS Bedrock",
		ModelEnv:     "BEDROCK_MODEL",
		DefaultModel: config.DefaultBedrockModel,
		// BEDROCK_MAX_TOKENS is the primary env the Bedrock client reads (it
		// also accepts ANTHROPIC_MAX_TOKENS as a secondary, handled client-side).
		MaxTokensEnv:      "BEDROCK_MAX_TOKENS",
		SecretEnv:         []string{"AWS_BEARER_TOKEN_BEDROCK"},
		FallbackMaxTokens: bedrockFallbackMaxTokens,
		// Modelos fora do catálogo: application inference profiles com ID
		// opaco, marketplace e lançamentos recentes. Sem esta regra eles
		// caíam no default genérico de 50K e a compactação de histórico
		// disparava em quase todo turno de agent — mesma classe de bug do
		// StackSpot (PR #1044). Claude desconhecido no Bedrock é lançamento
		// novo — a família atual (Fable 5, Sonnet 5, Opus 4.6+) é toda 1M e
		// os modelos antigos de 200K têm entrada própria no catálogo; um
		// overshoot é recuperável (compact + retry no context-too-long),
		// um undershoot compacta cedo demais a cada turno. O resto do
		// catálogo AWS (Nova, Llama, Mistral, DeepSeek) senta em 128K.
		FallbackContextWindow: func(model string) int {
			m := strings.ToLower(model)
			if strings.Contains(m, "anthropic") || strings.Contains(m, "claude") {
				return 1000000
			}
			return 128000
		},
	},
	{
		Name:         ProviderGoogleAI,
		DisplayName:  "Google Gemini",
		ModelEnv:     "GOOGLEAI_MODEL",
		DefaultModel: config.DefaultGoogleAIModel,
		MaxTokensEnv: "GOOGLEAI_MAX_TOKENS",
		SecretEnv:    []string{"GOOGLEAI_API_KEY", "GOOGLE_AI_API_KEY", "GEMINI_API_KEY"},
		PreferredAPI: PreferredAPI("gemini_api"),
		// Gemini 2.x todos a 65K output.
		FallbackMaxTokens:     fixedTokens(65536),
		FallbackContextWindow: fixedTokens(1000000),
	},
	{
		Name:         ProviderXAI,
		DisplayName:  "xAI (Grok)",
		ModelEnv:     "XAI_MODEL",
		DefaultModel: config.DefaultXAIModel,
		MaxTokensEnv: "XAI_MAX_TOKENS",
		SecretEnv:    []string{"XAI_API_KEY", "GROK_API_KEY"},
		// Grok 3/4 expõem ≥16K na prática; manter cap conservador
		// porque xAI não publica limite oficial.
		FallbackMaxTokens:     fixedTokens(16384),
		FallbackContextWindow: fixedTokens(128000),
	},
	{
		Name:                  ProviderZAI,
		DisplayName:           "ZAI (Zhipu AI)",
		ModelEnv:              "ZAI_MODEL",
		DefaultModel:          config.DefaultZAIModel,
		MaxTokensEnv:          "ZAI_MAX_TOKENS",
		SecretEnv:             []string{"ZAI_API_KEY", "ZHIPU_API_KEY"},
		FallbackMaxTokens:     fixedTokens(65535),
		FallbackContextWindow: fixedTokens(202752),
	},
	{
		Name:                  ProviderMoonshot,
		DisplayName:           "Moonshot (Kimi)",
		ModelEnv:              "MOONSHOT_MODEL",
		DefaultModel:          config.DefaultMoonshotModel,
		MaxTokensEnv:          "MOONSHOT_MAX_TOKENS",
		SecretEnv:             []string{"MOONSHOT_API_KEY"},
		FallbackMaxTokens:     fixedTokens(131072),
		FallbackContextWindow: fixedTokens(262144),
	},
	{
		Name:                  ProviderMiniMax,
		DisplayName:           "MiniMax",
		ModelEnv:              "MINIMAX_MODEL",
		DefaultModel:          config.DefaultMiniMaxModel,
		MaxTokensEnv:          "MINIMAX_MAX_TOKENS",
		SecretEnv:             []string{"MINIMAX_API_KEY"},
		FallbackMaxTokens:     fixedTokens(131072),
		FallbackContextWindow: fixedTokens(204800),
	},
	{
		// The model is an agent (realm/agent-id), so there is no *_MODEL env.
		Name:         ProviderStackSpot,
		DisplayName:  "StackSpot AI",
		MaxTokensEnv: "STACKSPOT_MAX_TOKENS",
		SecretEnv:    []string{"CLIENT_ID", "CLIENT_KEY", "STACKSPOT_CLIENT_ID", "STACKSPOT_CLIENT_SECRET"},
		// The StackSpot agent API ignores client-side max_tokens; this
		// value only feeds local bookkeeping, so keep it aligned with
		// the registry entry instead of artificially low.
		FallbackMaxTokens: fixedTokens(128000),
		// StackSpot agents sit on top of current frontier models (128K+
		// windows); the generic 50K default made compaction fire on
		// almost every agent turn.
		FallbackContextWindow: fixedTokens(128000),
	},
	{
		Name:                  ProviderOllama,
		DisplayName:           "Ollama (Local)",
		ModelEnv:              "OLLAMA_MODEL",
		DefaultModel:          config.DefaultOllamaModel,
		MaxTokensEnv:          "OLLAMA_MAX_TOKENS",
		FallbackMaxTokens:     fixedTokens(8192),
		FallbackContextWindow: fixedTokens(8192),
	},
	{
		Name:                  ProviderCopilot,
		DisplayName:           "GitHub Copilot",
		ModelEnv:              "COPILOT_MODEL",
		DefaultModel:          config.DefaultCopilotModel,
		MaxTokensEnv:          "COPILOT_MAX_TOKENS",
		AuthProvider:          "github-copilot",
		SecretEnv:             []string{"GITHUB_COPILOT_TOKEN"},
		FallbackMaxTokens:     fixedTokens(16384),
		FallbackContextWindow: fixedTokens(128000),
	},
	{
		Name:                  ProviderOpenRouter,
		DisplayName:           "OpenRouter",
		ModelEnv:              "OPENROUTER_MODEL",
		DefaultModel:          config.DefaultOpenRouterModel,
		MaxTokensEnv:          "OPENROUTER_MAX_TOKENS",
		SecretEnv:             []string{"OPENROUTER_API_KEY"},
		FallbackMaxTokens:     fixedTokens(16384),
		FallbackContextWindow: fixedTokens(128000),
	},
	{
		Name:         ProviderDevin,
		DisplayName:  "Devin",
		ModelEnv:     "DEVIN_MODEL",
		DefaultModel: config.DefaultDevinModel,
		SecretEnv:    []string{"DEVIN_API_KEY"},
		// Modelos fora do catálogo servidos pelo Devin CLI: teto do lado
		// do cliente é só bookkeeping (o backend impõe o real).
		FallbackMaxTokens: fixedTokens(32000),
		// Modelo desconhecido servido pelo Devin CLI: janela conservadora;
		// CHATCLI_CONTEXT_WINDOW é o escape para deployments que diferem.
		FallbackContextWindow: fixedTokens(200000),
	},
}

func init() {
	for i := range builtinProviders {
		builtinProviders[i].builtin = true
	}
}

var (
	providersMu sync.RWMutex
	// runtimeProviders holds providers registered after startup, keyed by
	// upper-case name.
	runtimeProviders = map[string]ProviderDescriptor{}
)

// providerNamePattern bounds runtime provider names to the shape every
// surface already accepts for LLM_PROVIDER (env var friendly, upper-case).
var providerNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)

// Providers returns every known provider: built-ins in their declared order,
// then runtime-registered providers sorted by name. The slice is a copy.
func Providers() []ProviderDescriptor {
	providersMu.RLock()
	defer providersMu.RUnlock()
	out := make([]ProviderDescriptor, 0, len(builtinProviders)+len(runtimeProviders))
	for _, d := range builtinProviders {
		out = append(out, d.clone())
	}
	names := make([]string, 0, len(runtimeProviders))
	for name := range runtimeProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, runtimeProviders[name].clone())
	}
	return out
}

// ProviderNames returns the names of Providers(), in the same order.
func ProviderNames() []string {
	all := Providers()
	names := make([]string, len(all))
	for i, d := range all {
		names[i] = d.Name
	}
	return names
}

// LookupProvider returns the descriptor for name (case-insensitive).
func LookupProvider(name string) (ProviderDescriptor, bool) {
	key := strings.ToUpper(strings.TrimSpace(name))
	for _, d := range builtinProviders {
		if d.Name == key {
			return d.clone(), true
		}
	}
	providersMu.RLock()
	defer providersMu.RUnlock()
	d, ok := runtimeProviders[key]
	return d.clone(), ok
}

// RegisterProvider adds or replaces a runtime provider. Built-in names cannot
// be shadowed, so a plugin can never impersonate a shipped provider.
func RegisterProvider(d ProviderDescriptor) error {
	d.Name = strings.ToUpper(strings.TrimSpace(d.Name))
	if !providerNamePattern.MatchString(d.Name) {
		return fmt.Errorf("catalog: invalid provider name %q (want %s)", d.Name, providerNamePattern)
	}
	if strings.TrimSpace(d.DisplayName) == "" {
		return fmt.Errorf("catalog: provider %q needs a display name", d.Name)
	}
	for _, b := range builtinProviders {
		if b.Name == d.Name {
			return fmt.Errorf("catalog: provider %q is built-in and cannot be replaced", d.Name)
		}
	}
	d.builtin = false
	d.SecretEnv = append([]string(nil), d.SecretEnv...)
	providersMu.Lock()
	defer providersMu.Unlock()
	runtimeProviders[d.Name] = d
	return nil
}

// UnregisterProvider removes a runtime provider; built-ins are untouched.
func UnregisterProvider(name string) {
	providersMu.Lock()
	defer providersMu.Unlock()
	delete(runtimeProviders, strings.ToUpper(strings.TrimSpace(name)))
}
