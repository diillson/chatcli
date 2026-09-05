package catalog

import (
	"os"
	"strconv"
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
	ProviderDevin           = "DEVIN"
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
	// CompactRatio, when set, is the share of ContextWindow at which the
	// history compactor fires for this model (0 = mode default; the
	// session's /autocompact override always wins).
	CompactRatio float64
}

// registry: lista plana para facilitar matching por provedor + id/alias
//
// IMPORTANT ordering rule: newer entries MUST be declared BEFORE older
// ones whose aliases share a prefix. Resolve() walks the registry in
// order and the first exact-or-alias hit wins; an older entry placed
// first will silently shadow newer variants whose IDs happen to start
// with the older entry's alias prefix. The covered_by tests in
// catalog_test.go pin this contract for the Claude Opus 4.x line and
// the same applies to GPT-5.x — gpt-5.6-* must be listed before gpt-5.5
// before gpt-5.4 before gpt-5.3-codex before gpt-5 (whose alias list
// includes "gpt-5.1" and other prefix-y strings).
var registry = []ModelMeta{
	// ── OpenAI GPT-5 family ──────────────────────────────────────────
	// gpt-5.6 (GA Jul 9 2026): three named tiers — Sol (flagship), Terra
	// (balanced) and Luna (fast/affordable). Platform API specs: all three
	// tiers share a 1.05M-token context window with 128K max output (same
	// profile as 5.5). The Codex OAuth backend serves a smaller runtime
	// window (372K per /codex/models); the catalog follows the family
	// precedent and records the platform API limit. OAuth/Codex: all three
	// slugs require the originator + User-Agent headers
	// (config.OpenAICodex*) — without both, the backend 404s on Luna.
	{
		// The generic "gpt-5.6" alias points at Sol (flagship, priority 1
		// on Codex). The other tiers' exact IDs win in Resolve()'s tier-1
		// pass, so this loose alias cannot shadow them.
		ID:              "gpt-5.6-sol",
		Aliases:         []string{"gpt-5.6-sol", "gpt-5.6"},
		DisplayName:     "GPT-5.6 Sol",
		Provider:        ProviderOpenAI,
		ContextWindow:   1050000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-5.6-terra",
		Aliases:         []string{"gpt-5.6-terra"},
		DisplayName:     "GPT-5.6 Terra",
		Provider:        ProviderOpenAI,
		ContextWindow:   1050000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gpt-5.6-luna",
		Aliases:         []string{"gpt-5.6-luna"},
		DisplayName:     "GPT-5.6 Luna",
		Provider:        ProviderOpenAI,
		ContextWindow:   1050000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
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
	// gpt-5.4 family — specs re-verified on developers.openai.com/api/docs/
	// models (Sep 2026): the base model runs the same 1,050,000 / 128K
	// profile as 5.5/5.6 (plus gpt-5.4-pro, Responses-only, $30/$180),
	// while mini and nano sit on the 400K / 128K profile — so the tiers
	// need their own entry. The mini entry MUST precede the base one:
	// "gpt-5.4" is a prefix of "gpt-5.4-mini" and would swallow it in the
	// tier-2 alias pass. The previous 200K / 100K values were guesses
	// inherited from the o-series and undersized every 5.x request.
	{
		ID:              "gpt-5.4-mini",
		Aliases:         []string{"gpt-5.4-mini", "gpt-5.4-nano"},
		DisplayName:     "GPT-5.4 mini",
		Provider:        ProviderOpenAI,
		ContextWindow:   400000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "json_mode", "tools"},
	},
	{
		ID:              "gpt-5.4",
		Aliases:         []string{"gpt-5.4", "gpt-5.4-pro"},
		DisplayName:     "GPT-5.4",
		Provider:        ProviderOpenAI,
		ContextWindow:   1050000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "json_mode", "tools"},
	},
	{
		// gpt-5.3-codex: 400K context (272K max input) / 128K output,
		// Responses-only. The bare "gpt-5.3" / "-mini" / "-nano" aliases
		// were dropped: no such models exist on the API (404 on the model
		// docs, absent from /models/all). gpt-5.3-codex-spark was removed
		// for the same reason — it is a Codex-app-only model
		// ("API Access: false"), never a platform id.
		ID:              "gpt-5.3-codex",
		Aliases:         []string{"gpt-5.3-codex"},
		DisplayName:     "GPT-5.3 Codex",
		Provider:        ProviderOpenAI,
		ContextWindow:   400000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"json_mode", "tools"},
	},
	{
		// gpt-5.2 (Dec 11 2025): 400K context / 128K output, Chat
		// Completions + Responses. gpt-5.2-pro shares the window
		// (Responses-only, $21/$168). No mini/nano tier was ever
		// published for 5.2 — those aliases were removed.
		ID:              "gpt-5.2",
		Aliases:         []string{"gpt-5.2", "gpt-5.2-pro"},
		DisplayName:     "GPT-5.2",
		Provider:        ProviderOpenAI,
		ContextWindow:   400000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIResponses,
		Capabilities:    []string{"vision", "json_mode", "tools"},
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
		Capabilities:    []string{"vision", "reasoning", "tools", "json_mode"},
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
		Capabilities:    []string{"vision", "reasoning", "tools", "json_mode"},
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
	// Retired on the Claude API (platform.claude.com/docs/en/about-claude/
	// model-deprecations, checked Sep 2 2026) and therefore removed from
	// the first-party block: Sonnet 4 and Opus 4 (Jun 15 2026), Opus 4.1
	// (Aug 5 2026) and every 3.x model (Sonnet 3.5 Oct 2025, Opus 3 Jan
	// 2026, Sonnet 3.7 / Haiku 3.5 Feb 2026, Haiku 3 Apr 2026). Requests
	// naming them fail server-side, so a catalog entry only misleads
	// sizing. Bedrock keeps its own lifecycle — the ones AWS still serves
	// stay in the ProviderBedrock block.
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
		Capabilities:    []string{"vision", "json_mode", "tools", "extended_thinking"},
	},
	{
		// Sonnet 4.6: 1M context, 128K max output on the synchronous
		// Messages API (platform.claude.com/docs/en/models/sonnet-4-6/
		// overview, Sep 2026; 300K on Batch with the output-300k beta).
		// The earlier 64K value was the pre-Mar-2026 limit and throttled
		// long agent turns for no reason.
		ID:              "claude-sonnet-4-6",
		Aliases:         []string{"claude-4-6-sonnet", "sonnet-4-6", "claude-4-6-sonnet-", "claude-sonnet-4-6-"},
		DisplayName:     "Claude sonnet 4.6 (1M context)",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"vision", "json_mode", "tools", "extended_thinking"},
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
		Capabilities:    []string{"vision", "json_mode", "tools"},
	},
	// NOTE: Claude 5.x/4.x entries are ordered newest-first below. The
	// Resolve() tier-2 alias match iterates the registry in order and
	// returns on the first prefix/contains hit, so an older entry whose
	// alias is a prefix of a newer id ("fable-5" ⊂ "fable-5-1") MUST come
	// after the newer one. Reversing this order silently resolves
	// "fable-5-1" to the Fable 5 entry (wrong cache price, wrong
	// capability flags).
	{
		// Fable 5.1 (claude-fable-5-1, Sep 1 2026): successor to Fable 5 in
		// the same tier above Opus, same $10/$50 per MTok, but cache reads
		// at $0.25/MTok (2.5% of input instead of the usual 10% — see
		// cli/cost_tracker.go getCachePricing). 1M context, 128K max
		// output, thinking ALWAYS on (adaptive; an explicit
		// thinking:{type:"disabled"} or budget_tokens returns 400 — the
		// claudeai client omits the field unless effort routing fires).
		// Three breaking changes vs Fable 5 (platform.claude.com/docs/en/
		// models/fable-5-1/whats-new-fable-5-1): forced tool_choice
		// ("any"/"tool") returns 400 — this client only ever sends
		// tool_choice auto; thinking blocks are bound to the producing
		// model (other models drop them silently); editing earlier turns
		// invalidates thinking blocks. mid_conversation_system is served
		// (same as Fable 5 / Opus 5). No fast_mode, no Priority Tier.
		// Requires 30-day data retention (ZDR orgs get 400).
		// The bare "fable" shortcut tracks the newest Fable release.
		ID:              "claude-fable-5-1",
		Aliases:         []string{"claude-fable-5-1", "fable-5-1", "claude-fable-5.1", "fable-5.1", "fable"},
		DisplayName:     "Claude Fable 5.1 (1M context)",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities: []string{
			"vision",
			"json_mode", "tools",
			"adaptive_thinking", "output_effort", "task_budget", "mid_conversation_system",
		},
	},
	{
		// Fable 5 (claude-fable-5, Jun 9 2026): legacy since Fable 5.1
		// shipped, still served (retirement not before Jun 9 2027). Same
		// tier above Opus: 1M context, 128K max output, $10/$50 per MTok
		// (see cli/cost_tracker.go claudePricing). Same API surface as
		// Opus 4.7/4.8 (no temperature/top_p/top_k; adaptive thinking only —
		// budgeted thinking returns 400) with one extra constraint: an
		// explicit thinking:{type:"disabled"} ALSO returns 400 — the field
		// must be omitted to run without thinking. The claudeai client
		// already complies: it only attaches a thinking block when effort
		// routing fires, and adaptive_thinking routes that to
		// {type:"adaptive"}. No fast_mode: the speed parameter is not
		// documented for Fable 5. "fable-5" stays pinned here; the bare
		// "fable" shortcut moved to 5.1.
		ID:              "claude-fable-5",
		Aliases:         []string{"claude-fable-5", "fable-5"},
		DisplayName:     "Claude Fable 5 (1M context)",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities: []string{
			"vision",
			"json_mode", "tools",
			"adaptive_thinking", "output_effort", "task_budget", "mid_conversation_system",
		},
	},
	{
		// Opus 5 (claude-opus-5, Jul 2026): the Opus-tier successor to 4.8,
		// positioned for complex agentic coding and enterprise work (Fable 5
		// remains the tier above). 1M context, 128K max output, $5/$25 per
		// MTok — same price as Opus 4.5-4.8 (see cli/cost_tracker.go
		// claudePricing, which needs the explicit opus-5 case to avoid the
		// legacy $15/$75 generic-opus fallthrough). Same API surface as
		// Opus 4.8: adaptive thinking only (budgeted thinking returns 400),
		// no temperature/top_p/top_k; effort defaults to "high" server-side.
		// No fast_mode (not documented for Opus 5) and no
		// mid_conversation_system (documented for Opus 4.8 only). Dateless
		// pinned-snapshot ID per the Jul 2026 models overview.
		ID:              "claude-opus-5",
		Aliases:         []string{"claude-opus-5", "opus-5", "claude-5-opus"},
		DisplayName:     "Claude Opus 5 (1M context)",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"vision", "json_mode", "tools", "adaptive_thinking", "output_effort", "task_budget"},
	},
	{
		// Sonnet 5 (claude-sonnet-5, Jun 2026): the Sonnet-tier successor —
		// Anthropic skipped a "Sonnet 4.7"/"4.8" and jumped straight to 5.
		// 1M context, 128K max output, adaptive thinking (effort defaults to
		// "high" server-side on the Claude API). $2/$10 per MTok: the
		// launch "introductory" rate became the permanent list price —
		// Anthropic cancelled the Sep 1 2026 increase to $3/$15
		// (platform.claude.com/docs/en/about-claude/pricing), so
		// cost_tracker's claudePricing has an explicit sonnet-5 case.
		// No fast_mode (Opus-tier research preview only) and no
		// mid_conversation_system (documented for Opus 4.8 only). Dateless
		// pinned-snapshot ID per the Jun 2026 models overview.
		ID:              "claude-sonnet-5",
		Aliases:         []string{"claude-sonnet-5", "sonnet-5", "claude-5-sonnet"},
		DisplayName:     "Claude Sonnet 5 (1M context)",
		Provider:        ProviderClaudeAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		APIVersion:      config.ClaudeAIAPIVersionDefault,
		Capabilities:    []string{"vision", "json_mode", "tools", "adaptive_thinking", "output_effort", "task_budget"},
	},
	{
		// Opus 4.8 (claude-opus-4-8, May 28 2026): 1M context by default on
		// the Claude API, 128K max output. Same API constraints as 4.7
		// (no temperature/top_p/top_k; adaptive thinking only — extended
		// thinking budgets return 400). New capabilities at launch:
		//   - "adaptive_thinking", "output_effort", "task_budget": only supported thinking mode
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
			"vision",
			"json_mode", "tools",
			"adaptive_thinking", "output_effort", "task_budget", "fast_mode",
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
		Capabilities:    []string{"vision", "json_mode", "tools", "adaptive_thinking", "output_effort", "task_budget"},
	},
	// There is no Sonnet 4.7: Anthropic went from Sonnet 4.6 straight to
	// Sonnet 5. The forward-projected placeholder that used to sit here
	// was removed once the Sep 2026 models overview confirmed it never
	// shipped — "sonnet-4-7" now resolves to nothing, as it should.
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
		Capabilities:    []string{"vision", "json_mode", "tools", "extended_thinking"},
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
		Capabilities:    []string{"vision", "json_mode", "tools", "extended_thinking"},
	},
	// Opus 4.1 (retired Aug 5 2026), Opus 4 (Jun 15 2026) and the whole
	// Claude 3.x line used to sit here — see the retirement note above
	// the Sonnet 4.5 entry.
	// Google Gemini Models. Specs from ai.google.dev model docs:
	// every Gemini 2.x/3.x exposes a 1,048,576-token input window with a
	// 65,536-token max output (8,192 on the older 2.0 generation). The
	// previous catalog set MaxOutputTokens equal to ContextWindow, which
	// is physically impossible — the API rejects requests with output
	// over the per-model cap.
	//
	// Gemini 3.x generation (re-verified per-model on ai.google.dev, Sep 2
	// 2026 — nothing newer than 3.7 Flash has shipped): 3.7-flash (GA Aug
	// 13 2026), 3.6-flash (Jul 21 2026), 3.5-flash, 3.5-flash-lite and
	// 3.1-flash-lite stable; 3.1-pro-preview and 3-flash-preview in
	// preview. "gemini-3.1-pro" without -preview is NOT a real model code
	// (kept only as an alias). The whole generation shares the uniform
	// 1,048,576 / 65,536 profile. Newest first so generic alias prefixes
	// don't shadow the more specific ids.
	{
		ID:              "gemini-3.7-flash",
		Aliases:         []string{"gemini-3.7-flash", "gemini-3.7-flash-latest"},
		DisplayName:     "Gemini 3.7 Flash",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "code_execution"},
	},
	{
		ID:              "gemini-3.6-flash",
		Aliases:         []string{"gemini-3.6-flash", "gemini-3.6-flash-latest"},
		DisplayName:     "Gemini 3.6 Flash",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "code_execution"},
	},
	{
		ID:              "gemini-3.5-flash",
		Aliases:         []string{"gemini-3.5-flash", "gemini-3.5-flash-latest"},
		DisplayName:     "Gemini 3.5 Flash",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "code_execution"},
	},
	{
		ID:              "gemini-3.5-flash-lite",
		Aliases:         []string{"gemini-3.5-flash-lite"},
		DisplayName:     "Gemini 3.5 Flash Lite",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gemini-3.1-pro-preview",
		Aliases:         []string{"gemini-3.1-pro-preview", "gemini-3.1-pro", "gemini-3.1-pro-preview-customtools"},
		DisplayName:     "Gemini 3.1 Pro (preview)",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode", "code_execution"},
	},
	{
		ID:              "gemini-3.1-flash-lite",
		Aliases:         []string{"gemini-3.1-flash-lite"},
		DisplayName:     "Gemini 3.1 Flash Lite",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "gemini-3-flash-preview",
		Aliases:         []string{"gemini-3-flash-preview"},
		DisplayName:     "Gemini 3 Flash (preview)",
		Provider:        ProviderGoogleAI,
		ContextWindow:   1048576,
		MaxOutputTokens: 65536,
		PreferredAPI:    "gemini_api",
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
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
	// Shut down by Google (ai.google.dev/gemini-api/docs/deprecations,
	// checked Sep 2 2026) and removed from ProviderGoogleAI:
	// gemini-2.0-flash / gemini-2.0-flash-lite (Jun 1 2026) and
	// gemini-3-pro-preview (Mar 9 2026, replaced by gemini-3.1-pro-preview).
	// Next scheduled: gemini-3.1-flash-lite retires May 7 2027 → migrate
	// to gemini-3.5-flash-lite. The Copilot-side gemini-2.0-flash entries
	// below follow GitHub's lifecycle, not Google's, and stay.
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
		Capabilities:    []string{"vision", "tools", "extended_thinking"},
	},
	{
		ID:              "gemini-2.0-flash",
		Aliases:         []string{"copilot-gemini-2.0-flash"},
		DisplayName:     "Gemini 2.0 Flash (Copilot)",
		Provider:        ProviderCopilot,
		ContextWindow:   1000000,
		MaxOutputTokens: 8192,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools"},
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
	//
	// 2026 generation (context windows per docs.x.ai/developers/models,
	// Aug 2026): grok-4.6 500K (flagship, Aug 12 2026), grok-4.5 500K,
	// grok-4.3 1M, the grok-4.20 family 1M, grok-build 256K. xAI still
	// documents no separate output cap — the 16K ceiling below follows
	// the same convention as the older entries. Newest first; the entry
	// carrying the generic "grok-4.20" alias sits after the more specific
	// 4.20 variants so it cannot shadow them. The announced "fast"
	// variant of 4.6 has no published model id yet — do not add it until
	// docs.x.ai lists one.
	{
		ID:              "grok-4.6",
		Aliases:         []string{"grok-4.6", "grok-4.6-latest"},
		DisplayName:     "Grok-4.6",
		Provider:        ProviderXAI,
		ContextWindow:   500000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "grok-4.5",
		Aliases:         []string{"grok-4.5", "grok-4.5-latest", "grok-build-latest"},
		DisplayName:     "Grok-4.5",
		Provider:        ProviderXAI,
		ContextWindow:   500000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "grok-4.3",
		Aliases:         []string{"grok-4.3", "grok-4.3-latest"},
		DisplayName:     "Grok-4.3",
		Provider:        ProviderXAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "grok-4.20-multi-agent-0309",
		Aliases:         []string{"grok-4.20-multi-agent-0309", "grok-4.20-multi-agent"},
		DisplayName:     "Grok-4.20 Multi-Agent",
		Provider:        ProviderXAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "grok-4.20-0309-non-reasoning",
		Aliases:         []string{"grok-4.20-0309-non-reasoning", "grok-4.20-non-reasoning"},
		DisplayName:     "Grok-4.20 (non-reasoning)",
		Provider:        ProviderXAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "grok-4.20-0309-reasoning",
		Aliases:         []string{"grok-4.20-0309-reasoning", "grok-4.20-reasoning", "grok-4.20"},
		DisplayName:     "Grok-4.20 (reasoning)",
		Provider:        ProviderXAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "grok-build-0.1",
		Aliases:         []string{"grok-build-0.1", "grok-build-0", "grok-code-fast-1", "grok-code-fast"},
		DisplayName:     "Grok Build 0.1",
		Provider:        ProviderXAI,
		ContextWindow:   256000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	// Retired by xAI on May 15 2026 (docs.x.ai/developers/migration/
	// may-15-retirement) and removed from ProviderXAI: grok-3, grok-3-mini,
	// grok-4-0709, grok-4-fast-* and grok-4-1-fast-* (the API now redirects
	// those slugs to grok-4.3 and bills grok-4.3 rates) and grok-code-fast-1
	// (now an alias of grok-build-0.1 — kept on that entry above so pinned
	// configs still size correctly). None of them has a model page or a
	// pricing row anymore.

	// ZAI (Zhipu AI / z.ai) Models
	// GLM-5 family. All entries below verified against Z.AI's per-model
	// docs (docs.z.ai/guides/llm/glm-*). Ordering rule: newest IDs first
	// so generic alias prefixes ("glm-5") don't shadow the more specific
	// "glm-5.1" / "glm-5-turbo" tags.
	{
		// GLM-5.3-Flash (Aug 26 2026): 1M-token context, 128K max output
		// (docs.z.ai/guides/llm/glm-5.3-flash). Natively multimodal
		// (images/videos/files), MIT license, thinking cannot be disabled.
		// Must sit ahead of glm-5.3 so the "-flash" tag is never shadowed
		// by the shorter alias in tier-2 (contains) matching.
		ID:              "glm-5.3-flash",
		Aliases:         []string{"glm-5.3-flash", "glm-5-3-flash"},
		DisplayName:     "GLM-5.3 Flash",
		Provider:        ProviderZAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode", "vision"},
	},
	{
		// GLM-5.3 (Aug 18 2026): 1M-token context, 128K max output
		// (docs.z.ai/guides/llm/glm-5.3). Same base model as GLM-5.2 with
		// scaled-up post-training (coding/agentic focus). Text-only input;
		// reasoning always on. Tool calling and structured output.
		ID:              "glm-5.3",
		Aliases:         []string{"glm-5.3", "glm-5-3"},
		DisplayName:     "GLM-5.3 (1M context)",
		Provider:        ProviderZAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		// GLM-5.2 (Jun 13 2026): 1M-token context, 128K max output
		// (docs.z.ai/guides/llm/glm-5.2). Open-weight MoE, MIT license,
		// tuned for coding/agentic workloads with thinking mode, function
		// calling and structured output.
		ID:              "glm-5.2",
		Aliases:         []string{"glm-5.2", "glm-5-2"},
		DisplayName:     "GLM-5.2 (1M context)",
		Provider:        ProviderZAI,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
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
	// codegeex-4 was removed: it is not served by the Z.AI international
	// API anymore (no pricing row, no model page — docs.z.ai 404s, Sep
	// 2026). GLM-4.x variants without an entry (glm-4.7-flash/-flashx,
	// glm-4.5-air/-airx/-x) resolve by prefix to their family entry
	// above, which already carries the right window.

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
	// Native multimodal MoE family from Moonshot AI. K3 is the flagship
	// (released Jul 2026): 2.8T total / 104B active params, 1M context via
	// Kimi Delta Attention, text+image+video input. Output stays at the
	// K2.x 128K cap — aggregator listings that show output = context are an
	// unbounded-listing artifact, not a real per-request cap. The
	// kimi-k2.7-code line (Jun 2026) is the coding-tuned K2.x: 256K
	// context, 32,768 default output per platform.kimi.ai/docs/pricing.
	// K2.6 (Apr 2026, 1T/32B, 256K) remains supported. Sunsets announced
	// on platform.kimi.ai/docs/models (Aug 2026): kimi-k2.5 and the
	// moonshot-v1-* series retire 2026-08-31, kimi-latest deprecated since
	// 2026-01-28 — entries stay until the dates pass so pinned configs
	// keep resolving.
	// Ordering: newest IDs first so generic aliases don't shadow specific
	// tags — "kimi-k2.7-code-highspeed" MUST precede "kimi-k2.7-code",
	// whose alias set would otherwise swallow it (Resolve matches aliases
	// by prefix/contains in registry order).
	{
		ID:              "kimi-k3",
		Aliases:         []string{"kimi-k3", "kimi-k-3", "k3", "k-3"},
		DisplayName:     "Kimi K3",
		Provider:        ProviderMoonshot,
		ContextWindow:   1048576,
		MaxOutputTokens: 131072,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "thinking", "json_mode"},
	},
	{
		ID:              "kimi-k2.7-code-highspeed",
		Aliases:         []string{"kimi-k2.7-code-highspeed", "kimi-k2-7-code-highspeed"},
		DisplayName:     "Kimi K2.7 Code (highspeed)",
		Provider:        ProviderMoonshot,
		ContextWindow:   262144,
		MaxOutputTokens: 32768,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "thinking", "json_mode"},
	},
	{
		ID:              "kimi-k2.7-code",
		Aliases:         []string{"kimi-k2.7-code", "kimi-k2-7-code", "kimi-k2.7", "kimi-k2-7", "k2.7", "k2-7"},
		DisplayName:     "Kimi K2.7 Code",
		Provider:        ProviderMoonshot,
		ContextWindow:   262144,
		MaxOutputTokens: 32768,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "thinking", "json_mode"},
	},
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
	// Retired by Moonshot (platform.kimi.ai/docs/models, platform
	// changelog — checked Sep 2 2026) and removed from ProviderMoonshot:
	// kimi-k2.5 and the whole moonshot-v1-* series (Aug 31 2026, now 404
	// "model not found"), kimi-k2-turbo-preview and the other K2
	// previews (May 25 2026), kimi-latest (Jan 28 2026) and
	// kimi-thinking-preview (Nov 11 2025). Migration target for all of
	// them is kimi-k3. Closes the dated follow-up in issue #1344.

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
	// Anthropic 5-family on OpenRouter (dateless slugs, 1M context). Listed
	// here so context-window sizing is right even before the dynamic
	// ListModels catalog loads — an uncataloged model falls back to the
	// 50K default and compacts constantly.
	{
		ID:              "anthropic/claude-opus-5",
		Aliases:         []string{"openrouter-claude-opus-5"},
		DisplayName:     "Claude Opus 5 (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "anthropic/claude-sonnet-5",
		Aliases:         []string{"openrouter-claude-sonnet-5"},
		DisplayName:     "Claude Sonnet 5 (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		// OpenRouter spells the 5.1 slug with a dot; listed before the
		// Fable 5 slug because "anthropic/claude-fable-5" is its prefix.
		ID:              "anthropic/claude-fable-5.1",
		Aliases:         []string{"openrouter-claude-fable-5.1", "openrouter-claude-fable-5-1", "anthropic/claude-fable-5-1"},
		DisplayName:     "Claude Fable 5.1 (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode"},
	},
	{
		ID:              "anthropic/claude-fable-5",
		Aliases:         []string{"openrouter-claude-fable-5"},
		DisplayName:     "Claude Fable 5 (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
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
		Capabilities:    []string{"vision", "tools", "json_mode", "extended_thinking"},
	},
	{
		ID:              "anthropic/claude-opus-4",
		Aliases:         []string{"openrouter-claude-opus-4"},
		DisplayName:     "Claude Opus 4 (OpenRouter)",
		Provider:        ProviderOpenRouter,
		ContextWindow:   200000,
		MaxOutputTokens: 32000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"vision", "tools", "json_mode", "extended_thinking"},
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
		Capabilities:    []string{"vision", "tools"},
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

	// NOTE (capabilities on Bedrock mirrors): fast_mode is a first-party
	// research preview and mid_conversation_system is not served by Bedrock
	// (Anthropic platform-availability matrix) — neither flag may appear on
	// ProviderBedrock entries or the client would emit parameters AWS
	// rejects. Per-block prompt caching (cache_control) IS supported for
	// Claude on Bedrock; only the top-level automatic cache parameter is
	// first-party-only, and this client never emits it.

	// Fable 5.1 (Sep 1 2026, AWS what's-new 2026/09 + Bedrock model card
	// anthropic-claude-fable-5-1). Same shape as Fable 5: dateless id,
	// us./global. profiles only, 1M / 128K, $10/$50 with $0.25 cache
	// reads, mandatory 30-day (aws_review) data retention — so the same
	// bedrock_mantle_only routing applies (InvokeModel under default
	// retention answers the same ValidationException Fable 5 does; the
	// Mantle→runtime fallback still covers accounts that opted in). MUST
	// precede the Fable 5 entry: "claude-fable-5" is a prefix of
	// "claude-fable-5-1" in the tier-2 alias pass.
	{
		ID:              "anthropic.claude-fable-5-1",
		Aliases:         []string{"bedrock-fable-5-1", "global.anthropic.claude-fable-5-1", "us.anthropic.claude-fable-5-1", "claude-fable-5-1", "fable-5-1", "claude-fable-5.1", "fable-5.1"},
		DisplayName:     "Claude Fable 5.1 (Bedrock, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "adaptive_thinking", "output_effort", "task_budget", "bedrock_mantle_only"},
	},
	// Fable 5 (Jun 9 2026). Dateless ID per Anthropic's Bedrock docs —
	// Fable 5, Opus 4.8 and Opus 4.7 have NO ARN-versioned model IDs on
	// Bedrock. Fable 5 is served EXCLUSIVELY by the Claude-in-Amazon-
	// Bedrock Messages endpoint: it requires 30-day data retention, which
	// only that agreement provides — legacy InvokeModel runs under the
	// account's default retention mode and answers 400 ValidationException
	// "data retention mode 'default' is not available for this model",
	// whatever profile prefix (us./global.) the caller uses. The
	// bedrock_mantle_only capability routes it through the Mantle path,
	// same as Sonnet 5.
	{
		ID:              "anthropic.claude-fable-5",
		Aliases:         []string{"bedrock-fable-5", "global.anthropic.claude-fable-5", "us.anthropic.claude-fable-5", "claude-fable-5", "fable-5"},
		DisplayName:     "Claude Fable 5 (Bedrock, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "adaptive_thinking", "output_effort", "task_budget", "bedrock_mantle_only"},
	},
	// Opus 5 (Jul 2026). Like Sonnet 5 and Fable 5, served through the
	// Claude-in-Amazon-Bedrock Messages endpoint (the models overview lists
	// the Bedrock id anthropic.claude-opus-5 with the Messages-API Bedrock
	// endpoint footnote) — bedrock_mantle_only routes it down the Mantle
	// path instead of InvokeModel.
	{
		ID:              "anthropic.claude-opus-5",
		Aliases:         []string{"bedrock-opus-5", "global.anthropic.claude-opus-5", "us.anthropic.claude-opus-5", "eu.anthropic.claude-opus-5", "au.anthropic.claude-opus-5", "claude-opus-5", "opus-5"},
		DisplayName:     "Claude Opus 5 (Bedrock, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "adaptive_thinking", "output_effort", "task_budget", "bedrock_mantle_only"},
	},
	// Sonnet 5 (Jun 2026). Served EXCLUSIVELY by the Claude-in-Amazon-
	// Bedrock Messages endpoint (bedrock-mantle.{region}.api.aws) — it has
	// no InvokeModel surface. The bedrock_mantle_only capability tells the
	// Bedrock client to route the request through the Mantle path; sending
	// this id to InvokeModel would return a ValidationException.
	{
		ID:              "anthropic.claude-sonnet-5",
		Aliases:         []string{"bedrock-sonnet-5", "global.anthropic.claude-sonnet-5", "us.anthropic.claude-sonnet-5", "eu.anthropic.claude-sonnet-5", "au.anthropic.claude-sonnet-5", "claude-sonnet-5", "sonnet-5"},
		DisplayName:     "Claude Sonnet 5 (Bedrock, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "adaptive_thinking", "output_effort", "task_budget", "bedrock_mantle_only"},
	},
	// Claude 4.8 (May 28 2026). Opus 4.8 = 1M / 128K per Anthropic.
	// Canonical id is the global. inference profile: the bare dateless id
	// has no on-demand surface — InvokeModel answers ValidationException
	// "retry with the ID or ARN of an inference profile", same pattern as
	// Opus 4.6. The previously-shipped dated profile IDs (…-20260528-v1:0)
	// never existed on AWS; they stay as aliases so pinned configs keep
	// resolving.
	{
		ID: "global.anthropic.claude-opus-4-8",
		Aliases: []string{
			"bedrock-opus-4-8", "anthropic.claude-opus-4-8",
			"global.anthropic.claude-opus-4-8-20260528-v1:0",
			"anthropic.claude-opus-4-8-20260528-v1:0", "claude-opus-4-8",
		},
		DisplayName:     "Claude Opus 4.8 (Bedrock, global, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities: []string{
			"tools", "vision", "json_mode",
			"adaptive_thinking", "output_effort", "task_budget", "low_cache_minimum",
		},
	},

	// Claude 4.7: Opus only — there is no Sonnet 4.7 on Bedrock (absent
	// from the model-cards index and from Anthropic's pricing table); the
	// forward-projected placeholder was dropped. Opus 4.7 = 1M / 128K
	// per Anthropic (dateless id, same rule as 4.8).
	{
		ID: "global.anthropic.claude-opus-4-7",
		Aliases: []string{
			"bedrock-opus-4-7", "anthropic.claude-opus-4-7",
			"global.anthropic.claude-opus-4-7-20260401-v1:0",
			"anthropic.claude-opus-4-7-20260401-v1:0", "claude-opus-4-7",
		},
		DisplayName:     "Claude Opus 4.7 (Bedrock, global, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "adaptive_thinking", "output_effort", "task_budget"},
	},

	// Claude 4.6 (abr 2026). Bedrock specs follow the AWS model cards:
	// Sonnet 4.6 = 1M / 64K ON BEDROCK (the first-party API raised it to
	// 128K in Mar 2026 but the Bedrock card still lists 64K — do not
	// mirror the CLAUDEAI value here), Opus 4.6 = 1M / 128K. Real Bedrock IDs
	// per the docs: anthropic.claude-sonnet-4-6 and
	// anthropic.claude-opus-4-6-v1 behind a global. inference profile.
	// The previously-shipped dated IDs (…-20260115-v1:0) never existed on
	// AWS; kept as aliases for pinned configs.
	{
		ID: "global.anthropic.claude-sonnet-4-6",
		Aliases: []string{
			"bedrock-sonnet-4-6", "anthropic.claude-sonnet-4-6",
			"global.anthropic.claude-sonnet-4-6-20260115-v1:0",
			"anthropic.claude-sonnet-4-6-20260115-v1:0", "claude-sonnet-4-6",
		},
		DisplayName:     "Claude Sonnet 4.6 (Bedrock, global, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "extended_thinking"},
	},
	{
		ID: "global.anthropic.claude-opus-4-6-v1",
		Aliases: []string{
			"bedrock-opus-4-6", "anthropic.claude-opus-4-6-v1",
			"global.anthropic.claude-opus-4-6-20260115-v1:0",
			"anthropic.claude-opus-4-6-20260115-v1:0", "claude-opus-4-6",
		},
		DisplayName:     "Claude Opus 4.6 (Bedrock, global, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "extended_thinking"},
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
		Capabilities:    []string{"tools", "vision", "json_mode", "extended_thinking"},
	},
	{
		ID:              "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		Aliases:         []string{"bedrock-sonnet-4-5-us"},
		DisplayName:     "Claude Sonnet 4.5 (Bedrock, us)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "extended_thinking"},
	},
	{
		// Opus 4.5 snapshot date is 20251101 (Bedrock model card and the
		// Anthropic overview). The previously shipped 20251001 spelling
		// never existed on AWS; it stays as an alias for pinned configs.
		ID:              "global.anthropic.claude-opus-4-5-20251101-v1:0",
		Aliases:         []string{"bedrock-opus-4-5", "anthropic.claude-opus-4-5-20251101-v1:0", "global.anthropic.claude-opus-4-5-20251001-v1:0", "anthropic.claude-opus-4-5-20251001-v1:0", "claude-opus-4-5"},
		DisplayName:     "Claude Opus 4.5 (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "extended_thinking"},
	},

	// Claude 4 / 4.1. Sonnet 4 has a real global. inference profile (AWS
	// launched global cross-region inference with it, Sep 2025) — canonical
	// goes global so /switch works from any region; the us./eu. regional
	// profiles stay as their own entries for operators pinning geography.
	// Opus 4 and 4.1 have NO global profile on AWS — us. remains the
	// broadest invokable spelling for them.
	{
		ID:              "global.anthropic.claude-sonnet-4-20250514-v1:0",
		Aliases:         []string{"bedrock-sonnet-4", "anthropic.claude-sonnet-4-20250514-v1:0", "claude-sonnet-4"},
		DisplayName:     "Claude Sonnet 4 (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "extended_thinking"},
	},
	{
		ID:              "us.anthropic.claude-sonnet-4-20250514-v1:0",
		Aliases:         []string{"bedrock-sonnet-4-us"},
		DisplayName:     "Claude Sonnet 4 (Bedrock, us)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "extended_thinking"},
	},
	{
		ID:              "eu.anthropic.claude-sonnet-4-20250514-v1:0",
		Aliases:         []string{"bedrock-sonnet-4-eu"},
		DisplayName:     "Claude Sonnet 4 (Bedrock, eu)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 64000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "extended_thinking"},
	},
	// Bedrock lifecycle (docs.aws.amazon.com/bedrock/latest/userguide/
	// model-lifecycle.html, Sep 2 2026): Sonnet 4 is Legacy with EOL Oct
	// 14 2026 and Opus 4.1 is Legacy with EOL Jan 8 2027 — both still
	// invokable today, so they stay. Opus 4 is retired on Bedrock and was
	// removed.
	{
		ID:              "us.anthropic.claude-opus-4-1-20250805-v1:0",
		Aliases:         []string{"bedrock-opus-4-1", "anthropic.claude-opus-4-1-20250805-v1:0", "claude-opus-4-1"},
		DisplayName:     "Claude Opus 4.1 (Bedrock, us)",
		Provider:        ProviderBedrock,
		ContextWindow:   200000,
		MaxOutputTokens: 32000,
		PreferredAPI:    APIAnthropicMessages,
		Capabilities:    []string{"tools", "vision", "json_mode", "extended_thinking"},
	},

	// Claude 3.x on Bedrock — all gone (model-cards index + lifecycle
	// table, Sep 2 2026): Sonnet 3.7 and Opus 3 retired, Haiku 3.5 past
	// its Jun 19 2026 EOL, Sonnet 3.5 v1/v2 off the cards (only a
	// "public extended access" pricing row remains), Haiku 3 EOL Sep 10
	// 2026. Their entries were removed; a stray 3.x id now falls back to
	// bedrockFallbackMaxTokens (64K for anthropic ids), which AWS rejects
	// with a clear ValidationException instead of silently sizing wrong.

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

	// ── AWS Bedrock — OpenAI GPT-5.6 (frontier, Jul 13 2026) ──────────
	// Sol/Terra/Luna on Bedrock (model cards openai-gpt-5-6-*) speak
	// Converse, Responses and Chat Completions but NOT InvokeModel, and
	// are served only through inference profiles (Sol: us./global.;
	// Terra/Luna: us./in./global.). The "openai." vendor prefix would
	// normally send them down the gpt-oss InvokeModel path, so these
	// entries carry bedrock_converse_only, which resolveFamily honors
	// ahead of the vendor sniff. 1,050,000 context / 128K output as on
	// the platform API (AWS lists "1M" and no output cap); prices per
	// card: Sol $4/$20, Terra $2/$12, Luna $0.20/$1.20 (global, ≤272K).
	{
		ID:              "global.openai.gpt-5.6-sol",
		Aliases:         []string{"bedrock-gpt-5.6-sol", "openai.gpt-5.6-sol", "us.openai.gpt-5.6-sol"},
		DisplayName:     "GPT-5.6 Sol (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   1050000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "json_mode", "bedrock_converse_only"},
	},
	{
		ID:              "global.openai.gpt-5.6-terra",
		Aliases:         []string{"bedrock-gpt-5.6-terra", "openai.gpt-5.6-terra", "us.openai.gpt-5.6-terra", "in.openai.gpt-5.6-terra"},
		DisplayName:     "GPT-5.6 Terra (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   1050000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "json_mode", "bedrock_converse_only"},
	},
	{
		ID:              "global.openai.gpt-5.6-luna",
		Aliases:         []string{"bedrock-gpt-5.6-luna", "openai.gpt-5.6-luna", "us.openai.gpt-5.6-luna", "in.openai.gpt-5.6-luna"},
		DisplayName:     "GPT-5.6 Luna (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   1050000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "json_mode", "bedrock_converse_only"},
	},

	// ── AWS Bedrock — Amazon Nova ────────────────────────────────────
	// Família nativa da AWS, roteada pela Converse API (que normaliza no
	// shape chat-completions). Os IDs primários usam o prefixo de
	// inference profile "us." (exigido para invocação on-demand na
	// maioria das regiões); os IDs base ficam como aliases. Sem estas
	// entradas as janelas de 300K/1M caíam no fallback genérico do
	// provider e a compactação disparava cedo demais. MaxOutputTokens
	// segue o teto documentado de 5K da família; BEDROCK_MAX_TOKENS
	// cobre deployments que exponham mais.
	{
		ID:              "us.amazon.nova-micro-v1:0",
		Aliases:         []string{"bedrock-nova-micro", "amazon.nova-micro-v1:0", "nova-micro"},
		DisplayName:     "Amazon Nova Micro (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   128000,
		MaxOutputTokens: 5120,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "json_mode"},
	},
	{
		ID:              "us.amazon.nova-lite-v1:0",
		Aliases:         []string{"bedrock-nova-lite", "amazon.nova-lite-v1:0", "nova-lite"},
		DisplayName:     "Amazon Nova Lite (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   300000,
		MaxOutputTokens: 5120,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	{
		ID:              "us.amazon.nova-pro-v1:0",
		Aliases:         []string{"bedrock-nova-pro", "amazon.nova-pro-v1:0", "nova-pro"},
		DisplayName:     "Amazon Nova Pro (Bedrock)",
		Provider:        ProviderBedrock,
		ContextWindow:   300000,
		MaxOutputTokens: 5120,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},
	// Nova Premier (us.amazon.nova-premier-v1:0) is Legacy since Mar 13
	// 2026 with EOL Sep 14 2026 — removed. Its successor on Bedrock is
	// Nova 2 Lite (model card amazon-nova-2-lite, Dec 2025): 1M context,
	// 64K max output, Converse + InvokeModel, served ONLY through
	// inference profiles (us./eu./jp./global. — no bare in-region id), so
	// the canonical spelling carries the global. prefix. Nova 2 Pro/Omni
	// are not on Bedrock (Nova Forge preview only) and stay out.
	{
		ID:              "global.amazon.nova-2-lite-v1:0",
		Aliases:         []string{"bedrock-nova-2-lite", "amazon.nova-2-lite-v1:0", "us.amazon.nova-2-lite-v1:0", "eu.amazon.nova-2-lite-v1:0", "jp.amazon.nova-2-lite-v1:0", "nova-2-lite"},
		DisplayName:     "Amazon Nova 2 Lite (Bedrock, global, 1M ctx)",
		Provider:        ProviderBedrock,
		ContextWindow:   1000000,
		MaxOutputTokens: 65536,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},

	// ── AWS Bedrock — xAI Grok ────────────────────────────────────────
	// Grok 4.6 landed on Bedrock on Aug 18 2026 (model card xai-grok-4-6):
	// Converse + InvokeModel + Responses, served only through us./global.
	// inference profiles. The "xai." vendor is not a Converse-only
	// vendor string, but resolveFamily already routes every non-Anthropic,
	// non-OpenAI id through Converse, which is the surface AWS documents
	// for it. 500K context; xAI publishes no output cap, so the 16K
	// convention from the ProviderXAI block applies.
	{
		ID:              "global.xai.grok-4.6",
		Aliases:         []string{"bedrock-grok-4.6", "xai.grok-4.6", "us.xai.grok-4.6", "bedrock-grok-4-6"},
		DisplayName:     "Grok 4.6 (Bedrock, global)",
		Provider:        ProviderBedrock,
		ContextWindow:   500000,
		MaxOutputTokens: 16384,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools", "vision", "json_mode"},
	},

	// ── StackSpot AI ─────────────────────────────────────────────────
	{
		// StackSpot's agent chat API does not accept max_tokens — the
		// platform decides output limits server-side based on the agent's
		// underlying foundation model. These values therefore only drive
		// client-side bookkeeping (compaction budget, ctx% footer,
		// /metrics); without this entry both fell back to the generic
		// 50K default, which made history compaction fire far too early.
		ID:              "StackSpotAI",
		Aliases:         []string{"stackspot"},
		DisplayName:     "StackSpot AI",
		Provider:        ProviderStackSpot,
		ContextWindow:   128000,
		MaxOutputTokens: 128000,
		PreferredAPI:    APIChatCompletions,
		Capabilities:    []string{"tools"},
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

	// 2a) match por provedor + alias EXATO. Roda antes do passe frouxo
	// para que um apelido curto de uma entrada anterior ("fable" na
	// Fable 5.1, que precisa vir antes da Fable 5) nunca engula, por
	// Contains, o apelido exato de outra entrada ("fable-5").
	for _, meta := range registry {
		if meta.Provider != "" && meta.Provider != p {
			continue
		}
		for _, alias := range meta.Aliases {
			if m == strings.ToLower(alias) {
				return meta, true
			}
		}
	}

	// 2b) match por provedor + aliases (contém/prefixo)
	for _, meta := range registry {
		if meta.Provider != "" && meta.Provider != p {
			continue
		}
		for _, alias := range meta.Aliases {
			a := strings.ToLower(alias)
			if strings.HasPrefix(m, a) || strings.Contains(m, a) {
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
	case ProviderBedrock:
		return bedrockFallbackMaxTokens(model)
	case ProviderStackSpot:
		// The StackSpot agent API ignores client-side max_tokens; this
		// value only feeds local bookkeeping, so keep it aligned with
		// the registry entry instead of artificially low.
		return 128000
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
	case ProviderDevin:
		// Modelos fora do catálogo servidos pelo Devin CLI: teto do lado
		// do cliente é só bookkeeping (o backend impõe o real).
		return 32000
	default:
		return 50000
	}
}

// bedrockFallbackMaxTokens escolhe o teto de saída para um modelo Bedrock
// que NÃO está no registry (application inference profiles com ARN opaco,
// marketplace, lançamento recém-anunciado). Converse/InvokeModel rejeitam
// maxTokens acima do cap real do modelo com ValidationException dura, então
// cada sniff de família assume o MENOR teto entre os modelos atuais daquela
// família; o default genérico cobre as famílias modernas (Llama, Mistral,
// Qwen — todas ≥8K) e as famílias com cap real menor (Nova, Titan, Cohere
// Command, AI21 Jamba) mantêm o próprio piso.
func bedrockFallbackMaxTokens(model string) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "anthropic") || strings.Contains(m, "claude"):
		// Claude desconhecido no Bedrock é lançamento novo com teto ≥64K —
		// os modelos 3.x de cap menor têm entrada própria no catálogo.
		return 64000
	case strings.Contains(m, "deepseek"):
		return 32768
	case strings.Contains(m, "openai") || strings.Contains(m, "gpt-oss"):
		return 16384
	case strings.Contains(m, "nova"):
		return 5120
	case strings.Contains(m, "titan-text-premier"):
		// Único da família Titan com cap real ABAIXO do antigo default de
		// 4096 — sem este sniff qualquer valor maior é erro duro.
		return 3072
	case strings.Contains(m, "titan-text-express"):
		return 8192
	case strings.Contains(m, "titan"):
		return 4096
	case strings.Contains(m, "cohere") || strings.Contains(m, "jamba") || strings.Contains(m, "ai21"):
		return 4096
	default:
		return 8192
	}
}

// GetContextWindow returns the context window size (in tokens) for the given
// provider+model. CHATCLI_CONTEXT_WINDOW, when set to a positive integer,
// overrides everything — the escape hatch for gateways/agents whose real
// window differs from the catalog (e.g. a StackSpot agent backed by a
// larger or smaller foundation model). Otherwise falls back to a
// conservative per-provider default when the model is not in the registry.
// GetCompactRatio returns the catalog's per-model auto-compact threshold as
// a share of the window, 0 when the entry sets none.
func GetCompactRatio(provider, model string) float64 {
	if meta, ok := Resolve(provider, model); ok && meta.CompactRatio > 0 && meta.CompactRatio <= 1 {
		return meta.CompactRatio
	}
	return 0
}

func GetContextWindow(provider, model string) int {
	if raw := os.Getenv("CHATCLI_CONTEXT_WINDOW"); raw != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
			return n
		}
	}
	if meta, ok := Resolve(provider, model); ok && meta.ContextWindow > 0 {
		return meta.ContextWindow
	}
	switch strings.ToUpper(provider) {
	case ProviderGoogleAI:
		return 1000000
	case ProviderClaudeAI:
		return 200000
	case ProviderOpenAI, ProviderOpenAIAssistant:
		return 128000
	case ProviderBedrock:
		// Modelos fora do catálogo: application inference profiles com ID
		// opaco, marketplace e lançamentos recentes. Sem este case eles
		// caíam no default genérico de 50K e a compactação de histórico
		// disparava em quase todo turno de agent — mesma classe de bug do
		// StackSpot (PR #1044). Claude desconhecido no Bedrock é lançamento
		// novo — a família atual (Fable 5, Sonnet 5, Opus 4.6+) é toda 1M e
		// os modelos antigos de 200K têm entrada própria no catálogo; um
		// overshoot é recuperável (compact + retry no context-too-long),
		// um undershoot compacta cedo demais a cada turno. O resto do
		// catálogo AWS (Nova, Llama, Mistral, DeepSeek) senta em 128K.
		m := strings.ToLower(model)
		if strings.Contains(m, "anthropic") || strings.Contains(m, "claude") {
			return 1000000
		}
		return 128000
	case ProviderStackSpot:
		// StackSpot agents sit on top of current frontier models (128K+
		// windows); the generic 50K default made compaction fire on
		// almost every agent turn.
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
	case ProviderDevin:
		// Modelo desconhecido servido pelo Devin CLI: janela conservadora;
		// CHATCLI_CONTEXT_WINDOW é o escape para deployments que diferem.
		return 200000
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
		capLower := strings.ToLower(capability)
		for _, c := range meta.Capabilities {
			if strings.ToLower(c) == capLower {
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
