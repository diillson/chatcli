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
	ProviderZAI             = "ZAI"
	ProviderMiniMax         = "MINIMAX"
	ProviderMoonshot        = "MOONSHOT"
	ProviderOllama          = "OLLAMA"
	ProviderCopilot         = "COPILOT"
	ProviderGitHubModels    = "GITHUB_MODELS"
	ProviderOpenRouter      = "OPENROUTER"
	ProviderBedrock         = "BEDROCK"
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
//
// IMPORTANT ordering rule: newer entries MUST be declared BEFORE older
// ones whose aliases share a prefix. Resolve() walks the registry in
// order and the first exact-or-alias hit wins; an older entry placed
// first will silently shadow newer variants whose IDs happen to start
// with the older entry's alias prefix. The covered_by tests in
// catalog_test.go pin this contract for the Claude Opus 4.x line and
// the same applies to GPT-5.x — gpt-5.5 must be listed before gpt-5.4
// before gpt-5.3-codex before gpt-5 (whose alias list includes "gpt-5.1"
// and other prefix-y strings).
var registry = []ModelMeta{
	// ── OpenAI GPT-5 family ──────────────────────────────────────────
	{
		// gpt-5.5 — released Apr 23, 2026. 1,050,000-token context with
		// 128,000 max output, Responses + Chat Completions + Assistants.
		// Capabilities: vision (input), function calling, structured
		// outputs. The codex/pro/mini/nano fan-out the previous
		// generations had is replaced by the single base model + a
		// distinct gpt-5.5-pro entry; OpenAI did not publish mini/nano
		// variants for 5.5 at launch.
		ID:              "gpt-5.5",
		Aliases:         []string{"gpt-5.5"},
		DisplayName:     "GPT-5.5",
		Provider:        ProviderOpenAI,
		ContextWindow:   1050000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		// gpt-5.5-pro — same window/output ceiling as 5.5 but no
		// streaming; intended for the highest-quality, batch-style
		// responses. Function calling and structured outputs are still
		// supported, vision is input-only.
		ID:              "gpt-5.5-pro",
		Aliases:         []string{"gpt-5.5-pro"},
		DisplayName:     "GPT-5.5 Pro",
		Provider:        ProviderOpenAI,
		ContextWindow:   1050000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-5.4",
		Aliases:         []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"},
		DisplayName:     "GPT-5.4",
		Provider:        ProviderOpenAI,
		ContextWindow:   200000,
		MaxOutputTokens: 100000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "gpt-5.3-codex",
		Aliases:         []string{"gpt-5.3-codex", "gpt-5.3", "gpt-5.3-mini", "gpt-5.3-nano"},
		DisplayName:     "GPT-5.3 Codex",
		Provider:        ProviderOpenAI,
		ContextWindow:   200000,
		MaxOutputTokens: 100000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "gpt-5.3-codex-spark",
		Aliases:         []string{"gpt-5.3-codex-spark"},
		DisplayName:     "GPT-5.3 Codex Spark (Pro)",
		Provider:        ProviderOpenAI,
		ContextWindow:   200000,
		MaxOutputTokens: 100000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		ID:              "gpt-5.2",
		Aliases:         []string{"gpt-5.2", "gpt-5.2-mini", "gpt-5.2-nano"},
		DisplayName:     "GPT-5.2",
		Provider:        ProviderOpenAI,
		ContextWindow:   200000,
		MaxOutputTokens: 100000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// gpt-5 (Aug 7 2025), incl. mini/nano/pro: 400K context, 128K max
		// output. Previous catalog (128K/16K) was inherited from GPT-4o
		// and undercounted by ~3× on context, ~8× on output.
		ID:              "gpt-5",
		Aliases:         []string{"gpt-5", "gpt-5.1", "gpt-5-mini", "gpt-5-nano", "gpt-5-pro"},
		DisplayName:     "GPT-5",
		Provider:        ProviderOpenAI,
		ContextWindow:   400000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"json_mode", "tools", "vision"},
	},
	// ── OpenAI o-series reasoning models ──────────────────────────
	{
		ID:              "o3",
		Aliases:         []string{"o3"},
		DisplayName:     "o3",
		Provider:        ProviderOpenAI,
		ContextWindow:   200000,
		MaxOutputTokens: 100000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"reasoning", "tools", "json_mode"},
	},
	{
		ID:              "o3-mini",
		Aliases:         []string{"o3-mini"},
		DisplayName:     "o3 mini",
		Provider:        ProviderOpenAI,
		ContextWindow:   200000,
		MaxOutputTokens: 100000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"reasoning", "tools", "json_mode"},
	},
	{
		ID:              "o4-mini",
		Aliases:         []string{"o4-mini"},
		DisplayName:     "o4 mini",
		Provider:        ProviderOpenAI,
		ContextWindow:   200000,
		MaxOutputTokens: 100000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"reasoning", "tools", "json_mode"},
	},
	// ── OpenAI GPT-4.1 family ─────────────────────────────────────
	{
		ID:              "gpt-4.1",
		Aliases:         []string{"gpt-4.1"},
		DisplayName:     "GPT-4.1",
		Provider:        ProviderOpenAI,
		ContextWindow:   1047576,
		MaxOutputTokens: 32768,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-4.1-mini",
		Aliases:         []string{"gpt-4.1-mini"},
		DisplayName:     "GPT-4.1 mini",
		Provider:        ProviderOpenAI,
		ContextWindow:   1047576,
		MaxOutputTokens: 32768,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-4.1-nano",
		Aliases:         []string{"gpt-4.1-nano"},
		DisplayName:     "GPT-4.1 nano",
		Provider:        ProviderOpenAI,
		ContextWindow:   1047576,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	// ── OpenAI GPT-4o family (legacy, Chat Completions) ───────────
	{
		ID:              "gpt-4o",
		Aliases:         []string{"gpt-4o"},
		DisplayName:     "GPT-4o",
		Provider:        ProviderOpenAI,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-4o-mini",
		Aliases:         []string{"gpt-4o-mini"},
		DisplayName:     "GPT-4o mini",
		Provider:        ProviderOpenAI,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	// Claude 4 e 4.1 (sonnet/opus). Specs grounded in Anthropic's official
	// models page (platform.claude.com/docs/en/docs/about-claude/models/overview).
	{
		// Sonnet 4 (claude-sonnet-4-20250514): deprecated, retires Jun 15
		// 2026. Real specs: 200K context, 64K max output. Previous catalog
		// value (50K/50K) was a conservative override that masked the model's
		// actual capacity and caused premature compaction.
		ID:              "claude-sonnet-4",
		Aliases:         []string{"claude-4-sonnet", "sonnet-4-20250514", "claude-4-sonnet-"},
		DisplayName:     "Claude sonnet 4",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// Sonnet 4.5: 200K context, 64K max output. 1M context available
		// via beta header; default registry tracks the GA limit.
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
		// Sonnet 4.6: 1M context, 64K max output (per Anthropic). Previous
		// catalog (200K/128K) had output inflated and ctx undersized.
		ID:              "claude-sonnet-4-6",
		Aliases:         []string{"claude-4-6-sonnet", "sonnet-4-6", "claude-4-6-sonnet-", "claude-sonnet-4-6-"},
		DisplayName:     "Claude sonnet 4.6 (1M context)",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// Haiku 4.5 (claude-haiku-4-5-20251001): 200K context, 64K max
		// output. Newest haiku in the GA matrix; was missing from the
		// ProviderClaudeAI side of the registry (only Bedrock had it).
		ID:              "claude-haiku-4-5-20251001",
		Aliases:         []string{"claude-haiku-4-5", "haiku-4-5", "claude-4-5-haiku"},
		DisplayName:     "Claude haiku 4.5",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	// NOTE: Claude 4.x entries are ordered newest-first below. The Resolve()
	// tier-2 alias match iterates the registry in order and returns on first
	// hit. Because the 4.0 entry carries the generic alias "opus-4", which
	// is a prefix of "opus-4-5/6/7/8", the newer entries MUST be iterated
	// first so their exact aliases (e.g. "opus-4-8") win before 4.0's
	// loose prefix match fires. Reversing this order reintroduces a latent
	// bug where "opus-4-6" silently resolves to the 4.0 entry (20K ctx).
	{
		// Opus 4.8 (claude-opus-4-8, May 28 2026): 1M context by default on
		// the Claude API, 128K max output. Same API constraints as 4.7
		// (no temperature/top_p/top_k; adaptive thinking only — extended
		// thinking budgets return 400). New capabilities at launch:
		//   - "adaptive_thinking": only supported thinking mode
		//   - "fast_mode": research-preview "speed":"fast" for ~2.5x output
		//     tokens per second at premium pricing
		//   - "mid_conversation_system": role:"system" messages accepted
		//     after the first user turn without breaking prompt cache
		//   - "low_cache_minimum": 1,024-token cacheable prompt floor
		//     (down from Opus 4.7).
		// Capability flags are read by the claudeai client + skill-router
		// to decide whether to emit `speed`, `thinking:{type:"adaptive"}`,
		// or mid-conversation system blocks.
		ID:              "claude-opus-4-8",
		Aliases:         []string{"claude-opus-4-8", "opus-4-8"},
		DisplayName:     "Claude opus 4.8 (1M context)",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities: []string{
			"json_mode", "tools",
			"adaptive_thinking", "fast_mode",
			"mid_conversation_system", "low_cache_minimum",
		},
	},
	{
		// Opus 4.7: 1M context, 128K max output. Same API constraints as
		// 4.8 (adaptive thinking only; no temp/top_p/top_k). Older catalog
		// did not flag adaptive_thinking explicitly — the claudeai client
		// now uses the capability flag to route between budgeted thinking
		// and the adaptive mode required by 4.7+.
		ID:              "claude-opus-4-7",
		Aliases:         []string{"claude-opus-4-7", "opus-4-7"},
		DisplayName:     "Claude opus 4.7 (1M context)",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools", "adaptive_thinking"},
	},
	{
		// Sonnet 4.7: NOT in the Anthropic GA matrix as of Apr 2026 (the
		// current shipping line is Opus 4.7 + Sonnet 4.6 + Haiku 4.5,
		// per https://platform.claude.com/docs/en/about-claude/models/overview).
		// This entry is a forward-projected alias placeholder so existing
		// tests and any caller pinning the tag don't break; the profile
		// mirrors Sonnet 4.6 (1M / 64K). Replace with real specs the
		// moment Anthropic publishes them — do not treat this as ground
		// truth.
		ID:              "claude-sonnet-4-7",
		Aliases:         []string{"claude-4-7-sonnet", "sonnet-4-7", "claude-sonnet-4-7-"},
		DisplayName:     "Claude sonnet 4.7",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// Opus 4.6: 1M context, 128K max output (per Anthropic legacy
		// table). Previous 400K/64K underrepresented both dimensions.
		ID:              "claude-opus-4-6",
		Aliases:         []string{"claude-opus-4-6", "opus-4-6"},
		DisplayName:     "Claude opus 4.6 (1M context)",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// Opus 4.5 (claude-opus-4-5-20251101): 200K context, 64K max output.
		ID:              "claude-opus-4-5",
		Aliases:         []string{"claude-opus-4-5", "opus-4-5", "claude-opus-4-5-20251101"},
		DisplayName:     "Claude opus 4.5",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// Opus 4.1: 200K context, 32K max output. Previous 20K/20K was a
		// holdover from an early conservative override and made the model
		// trip auto-compact at ~80K chars unnecessarily.
		ID:              "claude-opus-4-1-20250805",
		Aliases:         []string{"claude-opus-4-1", "opus-4-1", "claude-opus-4-1-20250805"},
		DisplayName:     "Claude opus 4.1",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 32000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// Opus 4.0 (deprecated, retires Jun 15 2026): 200K context, 32K
		// max output. Same correction rationale as 4.1.
		ID:              "claude-opus-4-20250514",
		Aliases:         []string{"opus-4", "claude-opus-4-20250514"},
		DisplayName:     "Claude opus 4",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 32000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	// Claude 3.x — specs from Anthropic's legacy/3.x docs. All 3.x models
	// share the 200K input window; max output varies by version.
	{
		// Sonnet 3.5 (claude-3-5-sonnet-20241022, "v2"): 200K / 8192.
		ID:              "claude-sonnet-3-5-20241022",
		Aliases:         []string{"claude-3-5-sonnet", "claude-3-5-sonnet-20241022"},
		DisplayName:     "Claude sonnet 3.5",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// Haiku 3.5 (claude-3-5-haiku-20241022): 200K / 8192. Was missing
		// from the ProviderClaudeAI side; only Bedrock had it.
		ID:              "claude-haiku-3-5-20241022",
		Aliases:         []string{"claude-3-5-haiku", "claude-3-5-haiku-20241022"},
		DisplayName:     "Claude haiku 3.5",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// Sonnet 3.7 (claude-3-7-sonnet-20250219): 200K context / 8192 max
		// output on the synchronous Messages API (the default any client
		// hits without opt-in headers). Extended thinking can raise
		// output to 64K, but only when the caller explicitly sends the
		// `output-128k-2025-02-19` beta header — pinning the catalog at
		// 64K silently throws 400 errors for every regular call. We
		// track the safe default; callers that opt into extended
		// thinking can override at request time.
		ID:              "claude-sonnet-3-7-20250219",
		Aliases:         []string{"claude-3-7-sonnet", "claude-3-7-sonnet-20250219"},
		DisplayName:     "Claude sonnet 3.7",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// Haiku 3 (claude-3-haiku-20240307): 200K / 4096.
		ID:              "claude-haiku-3",
		Aliases:         []string{"claude-3-haiku", "claude-3-haiku-20240307"},
		DisplayName:     "Claude haiku 3",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode"},
	},
	{
		// Opus 3 (claude-3-opus-20240229): 200K / 4096.
		ID:              "claude-opus-3",
		Aliases:         []string{"claude-3-opus", "claude-3-opus-20240229"},
		DisplayName:     "Claude opus 3",
		Provider:        ProviderClaudeAI,
		ContextWindow:   200000,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"json_mode", "tools"},
	},
	// Google Gemini Models. Specs from ai.google.dev model docs:
	// every Gemini 2.x exposes a 1,048,576-token input window with a
	// 65,536-token max output (8,192 on the older 2.0 generation). The
	// previous catalog set MaxOutputTokens equal to ContextWindow, which
	// is physically impossible — the API rejects requests with output
	// over the per-model cap.
	{
		ID:              "gemini-2.5-pro",
		Aliases:         []string{"gemini-2.5-pro", "gemini-2.5-pro-latest"},
		DisplayName:     "Gemini 2.5 Pro",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "code_execution"},
	},
	{
		// Gemini 3 Pro (preview): 1M context, 65K output. Conservative
		// projection until Google publishes 3.x specs.
		ID:              "gemini-3",
		Aliases:         []string{"gemini-3-pro", "gemini-3-pro-preview"},
		DisplayName:     "Gemini 3 Pro",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "code_execution"},
	},
	{
		ID:              "gemini-2.5-flash",
		Aliases:         []string{"gemini-2.5-flash"},
		DisplayName:     "Gemini 2.5 Flash",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gemini-2.5-flash-lite",
		Aliases:         []string{"gemini-2.5-flash-lite", "gemini-2.5-flash-lite"},
		DisplayName:     "Gemini 2.5 Flash Lite",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "multimodal_live"},
	},
	{
		// Gemini 2.0 Flash (deprecated): 1,048,576 input / 8,192 output.
		ID:              "gemini-2.0-flash",
		Aliases:         []string{"gemini-2.0-flash"},
		DisplayName:     "Gemini 2.0 Flash",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 8192,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		// Gemini 2.0 Flash Lite (deprecated): 1,048,576 input / 8,192 output.
		ID:              "gemini-2.0-flash-lite",
		Aliases:         []string{"gemini-2.0-flash-lite"},
		DisplayName:     "Gemini 2.0 Flash Lite",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 8192,
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

	// xAI (Grok) Models. Specs from xAI's published model docs and the
	// OpenRouter mirror (which xAI also publishes against). Aliases were
	// previously written as a single comma-joined string instead of
	// separate entries — Resolve() never matched them. Fixed here.
	{
		// grok-4-fast: 2M context. xAI does not document a separate output
		// cap; we ceil at 16K to keep the value comparable to other models
		// and avoid runaway generations on retry.
		ID:              "grok-4-fast",
		Aliases:         []string{"grok-4-fast-reasoning-latest", "grok-4-fast-reasoning", "grok-4-0709"},
		DisplayName:     "Grok-4 Fast",
		Provider:        ProviderXAI,
		ContextWindow:   2000000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		// grok-4-1: forward variant; tracked at the same 2M / 16K profile
		// until xAI publishes distinct specs.
		ID:              "grok-4-1",
		Aliases:         []string{"grok-4-1-fast", "grok-4-1-fast-reasoning-latest"},
		DisplayName:     "Grok-4-1",
		Provider:        ProviderXAI,
		ContextWindow:   2000000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		// grok-3: 131,072 context (128K), 16K max output.
		ID:              "grok-3",
		Aliases:         []string{"grok-3"},
		DisplayName:     "Grok-3",
		Provider:        ProviderXAI,
		ContextWindow:   131072,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"json_mode"},
	},
	{
		// grok-3-mini: same 131,072 / 16K profile as grok-3.
		ID:              "grok-3-mini",
		Aliases:         []string{"grok-3-mini"},
		DisplayName:     "Grok-3 Mini",
		Provider:        ProviderXAI,
		ContextWindow:   131072,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"json_mode"},
	},
	{
		// grok-code-fast-1: 256K context. Coding-tuned variant.
		ID:              "grok-code-fast-1",
		Aliases:         []string{"grok-code-fast-1"},
		DisplayName:     "Grok Code Fast 1",
		Provider:        ProviderXAI,
		ContextWindow:   256000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"json_mode"},
	},

	// ZAI (Zhipu AI / z.ai) Models
	// GLM-5 family. All entries below verified against Z.AI's per-model
	// docs (docs.z.ai/guides/llm/glm-*). Ordering rule: newest IDs first
	// so generic alias prefixes ("glm-5") don't shadow the more specific
	// "glm-5.1" / "glm-5-turbo" tags.
	{
		// GLM-5.1: 200K / 128K (docs.z.ai/guides/llm/glm-5.1).
		ID:              "glm-5.1",
		Aliases:         []string{"glm-5.1", "glm-5-1"},
		DisplayName:     "GLM-5.1",
		Provider:        ProviderZAI,
		ContextWindow:   200000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision"},
	},
	{
		// GLM-5-Turbo: 200K / 128K (docs.z.ai/guides/llm/glm-5-turbo).
		ID:              "glm-5-turbo",
		Aliases:         []string{"glm-5-turbo", "glm5-turbo"},
		DisplayName:     "GLM-5 Turbo",
		Provider:        ProviderZAI,
		ContextWindow:   200000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision"},
	},
	{
		// GLM-5: 200K / 128K (docs.z.ai/guides/llm/glm-5).
		ID:              "glm-5",
		Aliases:         []string{"glm-5"},
		DisplayName:     "GLM-5",
		Provider:        ProviderZAI,
		ContextWindow:   200000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision"},
	},
	{
		// GLM-4.7: 200K / 128K (docs.z.ai/guides/llm/glm-4.7).
		ID:              "glm-4.7",
		Aliases:         []string{"glm-4.7"},
		DisplayName:     "GLM-4.7",
		Provider:        ProviderZAI,
		ContextWindow:   200000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
	},
	{
		// GLM-4.6: 200K / 128K (docs.z.ai/guides/llm/glm-4.6).
		ID:              "glm-4.6",
		Aliases:         []string{"glm-4.6", "glm-4-6"},
		DisplayName:     "GLM-4.6",
		Provider:        ProviderZAI,
		ContextWindow:   200000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
	},
	{
		// GLM-4.5: 128K context, 96K max output.
		ID:              "glm-4.5",
		Aliases:         []string{"glm-4.5"},
		DisplayName:     "GLM-4.5",
		Provider:        ProviderZAI,
		ContextWindow:   128000,
		MaxOutputTokens: 96000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
	},
	{
		ID:              "glm-4.5-flash",
		Aliases:         []string{"glm-4.5-flash", "glm-4-flash"},
		DisplayName:     "GLM-4.5 Flash",
		Provider:        ProviderZAI,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
	},
	{
		ID:              "glm-5v-turbo",
		Aliases:         []string{"glm-5v-turbo"},
		DisplayName:     "GLM-5V Turbo",
		Provider:        ProviderZAI,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision"},
	},
	{
		ID:              "glm-4.5v",
		Aliases:         []string{"glm-4.5v", "glm-4-5v"},
		DisplayName:     "GLM-4.5V",
		Provider:        ProviderZAI,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision"},
	},
	{
		ID:              "codegeex-4",
		Aliases:         []string{"codegeex-4", "codegeex"},
		DisplayName:     "CodeGeeX-4",
		Provider:        ProviderZAI,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
	},

	// MiniMax Models
	{
		ID:              "MiniMax-M2.7",
		Aliases:         []string{"minimax-m2.7", "m2.7"},
		DisplayName:     "MiniMax M2.7",
		Provider:        ProviderMiniMax,
		ContextWindow:   204800,
		MaxOutputTokens: 131072,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision"},
	},
	{
		ID:              "MiniMax-M2.7-highspeed",
		Aliases:         []string{"minimax-m2.7-highspeed", "m2.7-highspeed"},
		DisplayName:     "MiniMax M2.7 Highspeed",
		Provider:        ProviderMiniMax,
		ContextWindow:   204800,
		MaxOutputTokens: 131072,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision"},
	},
	{
		ID:              "MiniMax-M2.5",
		Aliases:         []string{"minimax-m2.5", "m2.5"},
		DisplayName:     "MiniMax M2.5",
		Provider:        ProviderMiniMax,
		ContextWindow:   196608,
		MaxOutputTokens: 65536,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision"},
	},
	{
		ID:              "MiniMax-M2.5-highspeed",
		Aliases:         []string{"minimax-m2.5-highspeed", "m2.5-highspeed"},
		DisplayName:     "MiniMax M2.5 Highspeed",
		Provider:        ProviderMiniMax,
		ContextWindow:   196608,
		MaxOutputTokens: 65536,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision"},
	},
	{
		ID:              "MiniMax-Text-01",
		Aliases:         []string{"minimax-text-01", "text-01"},
		DisplayName:     "MiniMax Text-01",
		Provider:        ProviderMiniMax,
		ContextWindow:   128000,
		MaxOutputTokens: 2048,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"json_mode"},
	},

	// Moonshot (Kimi) Models.
	// Native multimodal MoE family from Moonshot AI. K2.6 is the flagship
	// (released Apr 2026): 1T total / 32B active params, 256K context, vision +
	// agent swarm + thinking mode. K2.5 is the previous generation, still
	// supported. moonshot-v1-* are the classic chat models split by context.
	// Ordering: newest IDs first so generic aliases don't shadow specific tags.
	{
		ID:              "kimi-k2.6",
		Aliases:         []string{"kimi-k2.6", "kimi-k2-6", "k2.6", "k2-6"},
		DisplayName:     "Kimi K2.6",
		Provider:        ProviderMoonshot,
		ContextWindow:   262144,
		MaxOutputTokens: 131072,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "thinking", "json_mode"},
	},
	{
		ID:              "kimi-k2.5",
		Aliases:         []string{"kimi-k2.5", "kimi-k2-5", "k2.5", "k2-5"},
		DisplayName:     "Kimi K2.5",
		Provider:        ProviderMoonshot,
		ContextWindow:   262144,
		MaxOutputTokens: 98304,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "thinking", "json_mode"},
	},
	{
		ID:              "kimi-latest",
		Aliases:         []string{"kimi-latest"},
		DisplayName:     "Kimi (latest)",
		Provider:        ProviderMoonshot,
		ContextWindow:   262144,
		MaxOutputTokens: 131072,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "thinking", "json_mode"},
	},
	{
		ID:              "kimi-k2-turbo-preview",
		Aliases:         []string{"kimi-k2-turbo-preview", "kimi-k2-turbo"},
		DisplayName:     "Kimi K2 Turbo (preview)",
		Provider:        ProviderMoonshot,
		ContextWindow:   262144,
		MaxOutputTokens: 65536,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		ID:              "kimi-thinking-preview",
		Aliases:         []string{"kimi-thinking-preview"},
		DisplayName:     "Kimi Thinking (preview)",
		Provider:        ProviderMoonshot,
		ContextWindow:   131072,
		MaxOutputTokens: 65536,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "thinking", "json_mode"},
	},
	{
		ID:              "moonshot-v1-128k",
		Aliases:         []string{"moonshot-v1-128k"},
		DisplayName:     "Moonshot v1 (128k)",
		Provider:        ProviderMoonshot,
		ContextWindow:   131072,
		MaxOutputTokens: 32768,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		ID:              "moonshot-v1-32k",
		Aliases:         []string{"moonshot-v1-32k"},
		DisplayName:     "Moonshot v1 (32k)",
		Provider:        ProviderMoonshot,
		ContextWindow:   32768,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		ID:              "moonshot-v1-8k",
		Aliases:         []string{"moonshot-v1-8k"},
		DisplayName:     "Moonshot v1 (8k)",
		Provider:        ProviderMoonshot,
		ContextWindow:   8192,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},

	// OpenRouter Models (multi-provider gateway)
	// Models use provider/model-name format. Only popular defaults are listed;
	// the full catalog is fetched dynamically via ListModels.
	{
		ID:              "openai/gpt-4o",
		Aliases:         []string{"openrouter-gpt-4o"},
		DisplayName:     "GPT-4o (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "openai/gpt-4o-mini",
		Aliases:         []string{"openrouter-gpt-4o-mini"},
		DisplayName:     "GPT-4o mini (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "anthropic/claude-sonnet-4",
		Aliases:         []string{"openrouter-claude-sonnet-4"},
		DisplayName:     "Claude Sonnet 4 (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		ID:              "anthropic/claude-opus-4",
		Aliases:         []string{"openrouter-claude-opus-4"},
		DisplayName:     "Claude Opus 4 (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   200000,
		MaxOutputTokens: 32000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		ID:              "google/gemini-2.5-pro",
		Aliases:         []string{"openrouter-gemini-2.5-pro"},
		DisplayName:     "Gemini 2.5 Pro (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   1000000,
		MaxOutputTokens: 65536,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "google/gemini-2.5-flash",
		Aliases:         []string{"openrouter-gemini-2.5-flash"},
		DisplayName:     "Gemini 2.5 Flash (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   1000000,
		MaxOutputTokens: 65536,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "meta-llama/llama-4-maverick",
		Aliases:         []string{"openrouter-llama-4-maverick"},
		DisplayName:     "Llama 4 Maverick (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
	},
	{
		// DeepSeek R1 via OpenRouter: served at 64K context per the
		// OpenRouter model card. R1 was deprecated upstream by DeepSeek
		// (replaced by deepseek-v4-flash / -pro at 1M); the entry is
		// kept for callers still pinning the old ID. Output ceiling
		// follows DeepSeek's published R1 spec (8K typical).
		ID:              "deepseek/deepseek-r1",
		Aliases:         []string{"openrouter-deepseek-r1"},
		DisplayName:     "DeepSeek R1 (OpenRouter, deprecated)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   64000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
	},
	{
		ID:              "mistralai/mistral-large",
		Aliases:         []string{"openrouter-mistral-large"},
		DisplayName:     "Mistral Large (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   128000,
		MaxOutputTokens: 32768,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},

	// ── AWS Bedrock — Anthropic Claude ───────────────────────────────
	// Modelos recentes (3.7, 4.x, 4.5, 4.6) NÃO aceitam invocação on-demand
	// pelo ID base — exigem um inference profile ID ("global.", "us.", "eu.",
	// "apac."). Por isso os IDs primários abaixo já vêm com o prefixo de
	// profile. Os IDs base ficam como aliases para ressolução por prefixo.
	// A listagem dinâmica via `bedrock:ListInferenceProfiles` complementa
	// este catálogo com o que a conta AWS realmente tem acesso.

	// Claude 4.6 (abr 2026 — mais recentes). Bedrock specs follow the
	// Anthropic models page: Sonnet 4.6 = 1M / 64K, Opus 4.6 = 1M / 128K.
	{
		ID:              "global.anthropic.claude-sonnet-4-6-20260115-v1:0",
		Aliases:         []string{"bedrock-sonnet-4-6", "anthropic.claude-sonnet-4-6-20260115-v1:0", "claude-sonnet-4-6"},
		DisplayName:     "Claude Sonnet 4.6 (Bedrock, global, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "global.anthropic.claude-opus-4-6-20260115-v1:0",
		Aliases:         []string{"bedrock-opus-4-6", "anthropic.claude-opus-4-6-20260115-v1:0", "claude-opus-4-6"},
		DisplayName:     "Claude Opus 4.6 (Bedrock, global, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	// Claude 4.8 (May 28 2026 — newest). Opus 4.8 = 1M / 128K per Anthropic.
	// Same on-demand restrictions as 4.7: needs an inference profile prefix
	// (global./us./eu./apac.) — the bare id is kept as an alias for
	// resolution by callers that pin the foundation-model name.
	{
		ID:              "global.anthropic.claude-opus-4-8-20260528-v1:0",
		Aliases:         []string{"bedrock-opus-4-8", "anthropic.claude-opus-4-8-20260528-v1:0", "claude-opus-4-8"},
		DisplayName:     "Claude Opus 4.8 (Bedrock, global, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities: []string{
			"tools", "vision", "json_mode",
			"adaptive_thinking", "fast_mode",
			"mid_conversation_system", "low_cache_minimum",
		},
	},

	// Claude 4.7. Sonnet 4.7 mirrors 4.6's profile (forward-projected),
	// Opus 4.7 = 1M / 128K per Anthropic.
	{
		ID:              "global.anthropic.claude-sonnet-4-7-20260401-v1:0",
		Aliases:         []string{"bedrock-sonnet-4-7", "anthropic.claude-sonnet-4-7-20260401-v1:0", "claude-sonnet-4-7"},
		DisplayName:     "Claude Sonnet 4.7 (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "global.anthropic.claude-opus-4-7-20260401-v1:0",
		Aliases:         []string{"bedrock-opus-4-7", "anthropic.claude-opus-4-7-20260401-v1:0", "claude-opus-4-7"},
		DisplayName:     "Claude Opus 4.7 (Bedrock, global, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "adaptive_thinking"},
	},
	{
		ID:              "global.anthropic.claude-haiku-4-5-20251001-v1:0",
		Aliases:         []string{"bedrock-haiku-4-5", "anthropic.claude-haiku-4-5-20251001-v1:0", "claude-haiku-4-5"},
		DisplayName:     "Claude Haiku 4.5 (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},

	// Claude 4.5
	{
		ID:              "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Aliases:         []string{"bedrock-sonnet-4-5", "anthropic.claude-sonnet-4-5-20250929-v1:0", "claude-sonnet-4-5"},
		DisplayName:     "Claude Sonnet 4.5 (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Aliases:         []string{"bedrock-sonnet-4-5-us"},
		DisplayName:     "Claude Sonnet 4.5 (Bedrock, us)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "global.anthropic.claude-opus-4-5-20251001-v1:0",
		Aliases:         []string{"bedrock-opus-4-5", "anthropic.claude-opus-4-5-20251001-v1:0", "claude-opus-4-5"},
		DisplayName:     "Claude Opus 4.5 (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},

	// Claude 4 / 4.1
	{
		ID:              "us.anthropic.claude-sonnet-4-20250514-v1:0",
		Aliases:         []string{"bedrock-sonnet-4", "anthropic.claude-sonnet-4-20250514-v1:0", "claude-sonnet-4"},
		DisplayName:     "Claude Sonnet 4 (Bedrock, us)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "eu.anthropic.claude-sonnet-4-20250514-v1:0",
		Aliases:         []string{"bedrock-sonnet-4-eu"},
		DisplayName:     "Claude Sonnet 4 (Bedrock, eu)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "us.anthropic.claude-opus-4-20250514-v1:0",
		Aliases:         []string{"bedrock-opus-4", "anthropic.claude-opus-4-20250514-v1:0", "claude-opus-4"},
		DisplayName:     "Claude Opus 4 (Bedrock, us)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 32000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "us.anthropic.claude-opus-4-1-20250805-v1:0",
		Aliases:         []string{"bedrock-opus-4-1", "anthropic.claude-opus-4-1-20250805-v1:0", "claude-opus-4-1"},
		DisplayName:     "Claude Opus 4.1 (Bedrock, us)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 32000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},

	// Claude 3.7 Sonnet (Bedrock mirrors). Same correction as the
	// upstream ProviderClaudeAI entry: synchronous Messages API caps
	// output at 8192. The 64K ceiling only unlocks via the
	// `output-128k-2025-02-19` extended-thinking beta header. Pinning
	// the catalog at the safe default keeps regular calls from being
	// rejected with 400 errors.
	{
		ID:              "us.anthropic.claude-3-7-sonnet-20250219-v1:0",
		Aliases:         []string{"bedrock-sonnet-3-7", "anthropic.claude-3-7-sonnet-20250219-v1:0", "claude-3-7-sonnet"},
		DisplayName:     "Claude 3.7 Sonnet (Bedrock, us)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "eu.anthropic.claude-3-7-sonnet-20250219-v1:0",
		Aliases:         []string{"bedrock-sonnet-3-7-eu"},
		DisplayName:     "Claude 3.7 Sonnet (Bedrock, eu)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},

	// Claude 3.5 — aceitam on-demand direto (sem prefixo)
	{
		ID:              "anthropic.claude-3-5-sonnet-20241022-v2:0",
		Aliases:         []string{"bedrock-sonnet-3-5-v2", "claude-3-5-sonnet-v2"},
		DisplayName:     "Claude 3.5 Sonnet v2 (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "anthropic.claude-3-5-sonnet-20240620-v1:0",
		Aliases:         []string{"bedrock-sonnet-3-5-v1"},
		DisplayName:     "Claude 3.5 Sonnet v1 (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "anthropic.claude-3-5-haiku-20241022-v1:0",
		Aliases:         []string{"bedrock-haiku-3-5", "claude-3-5-haiku"},
		DisplayName:     "Claude 3.5 Haiku (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "json_mode"},
	},

	// Claude 3 — legado (ainda suportado on-demand)
	{
		ID:              "anthropic.claude-3-opus-20240229-v1:0",
		Aliases:         []string{"bedrock-opus-3", "claude-3-opus"},
		DisplayName:     "Claude 3 Opus (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "anthropic.claude-3-haiku-20240307-v1:0",
		Aliases:         []string{"bedrock-haiku-3", "claude-3-haiku"},
		DisplayName:     "Claude 3 Haiku (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 4096,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},

	// ── AWS Bedrock — OpenAI GPT-OSS (open-weights) ──────────────────
	// Modelos OpenAI open-weights hospedados no Bedrock. Usam schema
	// OpenAI Chat Completions (distinto do Anthropic Messages).
	// O ChatCLI auto-detecta pelo prefixo "openai." do model id, ou
	// força via BEDROCK_PROVIDER=openai.
	{
		ID:              "openai.gpt-oss-120b-1:0",
		Aliases:         []string{"bedrock-gpt-oss-120b", "gpt-oss-120b"},
		DisplayName:     "GPT-OSS 120B (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		ID:              "openai.gpt-oss-20b-1:0",
		Aliases:         []string{"bedrock-gpt-oss-20b", "gpt-oss-20b"},
		DisplayName:     "GPT-OSS 20B (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   128000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
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

	// Fallbacks por provedor para modelos que NÃO estão no registry. Os
	// valores foram revistos contra a documentação oficial de cada
	// provedor (Apr 2026). O critério é "menor MaxOutput observado entre
	// os modelos atuais do provedor" — alto o suficiente para não
	// estrangular saídas legítimas, baixo o suficiente para um modelo
	// desconhecido não estourar limites do servidor.
	switch strings.ToUpper(provider) {
	case ProviderOpenAI:
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
	case ProviderClaudeAI:
		// Claude 3+ família toda usa 200K input; output varia 4K-128K.
		// 64K é o teto comum dos modelos atuais (sonnet 4.x, opus 4.5+).
		return 64000
	case ProviderStackSpot:
		return 50000
	case ProviderGoogleAI:
		// Gemini 2.x todos a 65K output.
		return 65536
	case ProviderXAI:
		// Grok 3/4 expõem ≥16K na prática; manter cap conservador
		// porque xAI não publica limite oficial.
		return 16384
	case ProviderZAI:
		return 65535
	case ProviderMiniMax:
		return 131072
	case ProviderMoonshot:
		return 131072
	case ProviderOllama:
		return 8192
	case ProviderCopilot:
		return 16384
	case ProviderGitHubModels:
		return 4096
	case ProviderOpenRouter:
		return 16384
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
	case ProviderZAI:
		return 202752
	case ProviderMiniMax:
		return 204800
	case ProviderMoonshot:
		return 262144
	case ProviderOllama:
		return 8192
	case ProviderCopilot:
		return 128000
	case ProviderGitHubModels:
		return 128000
	case ProviderOpenRouter:
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
