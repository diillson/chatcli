package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolve(t *testing.T) {
	testCases := []struct {
		name       string
		provider   string
		model      string
		expectedID string
		shouldFind bool
	}{
		{"Exact Match OpenAI", ProviderOpenAI, "gpt-4o-mini", "gpt-4o-mini", true},
		{"Alias Match ClaudeAI", ProviderClaudeAI, "claude-3-5-sonnet", "claude-sonnet-3-5-20241022", true},
		{"Prefix Match ClaudeAI", ProviderClaudeAI, "claude-3-5-sonnet-20241022-preview", "claude-sonnet-3-5-20241022", true},
		{"Case Insensitive", ProviderOpenAI, "GPT-4O", "gpt-4o", true},
		{"Gemini Flash Lite", ProviderGoogleAI, "gemini-2.0-flash-lite", "gemini-2.0-flash-lite", true},
		{"GPT-5 Alias", ProviderOpenAI, "gpt-5-mini", "gpt-5", true},
		{"Not Found", ProviderOpenAI, "gpt-nonexistent", "", false},
		{"Wrong Provider", ProviderStackSpot, "gpt-4o", "", false},
		// Regression: the bare "opus-4" alias on the 4.0 entry is a prefix
		// of all opus-4-X shortcuts. Newer entries MUST be declared first
		// in the registry so their exact-alias match wins over 4.0's
		// loose prefix match. Each of these silently resolved to the 4.0
		// entry (ctx=20K) before the fix.
		{"Claude Opus 4.5 shortcut", ProviderClaudeAI, "opus-4-5", "claude-opus-4-5", true},
		{"Claude Opus 4.6 shortcut", ProviderClaudeAI, "opus-4-6", "claude-opus-4-6", true},
		{"Claude Opus 4.7 shortcut", ProviderClaudeAI, "opus-4-7", "claude-opus-4-7", true},
		{"Claude Opus 4.7 full ID", ProviderClaudeAI, "claude-opus-4-7", "claude-opus-4-7", true},
		{"Claude Opus 4.8 shortcut", ProviderClaudeAI, "opus-4-8", "claude-opus-4-8", true},
		{"Claude Opus 4.8 full ID", ProviderClaudeAI, "claude-opus-4-8", "claude-opus-4-8", true},
		{"Claude Sonnet 4.7 shortcut", ProviderClaudeAI, "sonnet-4-7", "claude-sonnet-4-7", true},
		// Backward compat: bare "opus-4" still resolves to the 4.0 entry
		{"Claude Opus 4 bare alias", ProviderClaudeAI, "opus-4", "claude-opus-4-20250514", true},
		// gpt-5.5 family — released Apr 23 2026. Pin both the base and
		// the pro variant; the registry order also matters here so 5.5
		// is not shadowed by an earlier 5.x prefix match.
		{"GPT-5.5 exact", ProviderOpenAI, "gpt-5.5", "gpt-5.5", true},
		{"GPT-5.5 Pro exact", ProviderOpenAI, "gpt-5.5-pro", "gpt-5.5-pro", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			meta, found := Resolve(tc.provider, tc.model)
			assert.Equal(t, tc.shouldFind, found)
			if tc.shouldFind {
				assert.Equal(t, tc.expectedID, meta.ID)
			}
		})
	}
}

func TestGetMaxTokens(t *testing.T) {
	// Caso 1: Override tem precedência
	tokens := GetMaxTokens(ProviderOpenAI, "gpt-4o", 12345)
	assert.Equal(t, 12345, tokens, "Override should have the highest priority")

	// Caso 2: Valor do catálogo. Haiku 3 expõe 4096 tokens de output
	// (limite real publicado pela Anthropic, não o número conservador
	// inflado de 42K que o catálogo carregava antes da auditoria).
	tokens = GetMaxTokens(ProviderClaudeAI, "claude-3-haiku", 0)
	assert.Equal(t, 4096, tokens, "Should get value from catalog for claude-3-haiku")

	// Caso 3: Fallback para modelo desconhecido. Após a auditoria de
	// catálogo (Abr 2026) os fallbacks foram alinhados com os limites
	// oficiais — 16384 é o piso "OpenAI gpt-* genérico" para modelos
	// fora do registry, coerente com o cap real de gpt-4o (16K).
	tokens = GetMaxTokens(ProviderOpenAI, "unknown-model", 0)
	assert.Equal(t, 16384, tokens, "Should use fallback value for unknown OpenAI model")

	// Caso 4: Fallback para provider desconhecido
	tokens = GetMaxTokens("UNKNOWN_PROVIDER", "some-model", 0)
	assert.Equal(t, 50000, tokens, "Should use the default fallback value")

	// Caso 5: Moonshot lookup hits the explicit catalog entry for kimi-k2.6.
	tokens = GetMaxTokens(ProviderMoonshot, "kimi-k2.6", 0)
	assert.Equal(t, 131072, tokens, "kimi-k2.6 must report its catalog max_output_tokens")

	// Caso 6: Moonshot generic fallback when the model is absent from the catalog.
	tokens = GetMaxTokens(ProviderMoonshot, "kimi-future-model", 0)
	assert.Equal(t, 131072, tokens, "Moonshot fallback must come from the provider switch")
}

func TestGetContextWindow(t *testing.T) {
	// Known model hits the catalog entry.
	assert.Equal(t, 262144, GetContextWindow(ProviderMoonshot, "kimi-k2.6"))

	// Unknown moonshot model falls through the provider switch.
	assert.Equal(t, 262144, GetContextWindow(ProviderMoonshot, "kimi-future-v9"))

	// Unknown provider falls back to the conservative default.
	assert.Equal(t, 50000, GetContextWindow("UNKNOWN_PROVIDER", "x"))
}

func TestGetContextWindowEnvOverride(t *testing.T) {
	// A positive CHATCLI_CONTEXT_WINDOW beats both the catalog and the
	// provider fallbacks.
	t.Setenv("CHATCLI_CONTEXT_WINDOW", "32000")
	assert.Equal(t, 32000, GetContextWindow(ProviderClaudeAI, "claude-3-haiku"))
	assert.Equal(t, 32000, GetContextWindow("UNKNOWN_PROVIDER", "x"))

	// Garbage or non-positive values are ignored, not treated as 0.
	t.Setenv("CHATCLI_CONTEXT_WINDOW", "not-a-number")
	assert.Equal(t, 50000, GetContextWindow("UNKNOWN_PROVIDER", "x"))
	t.Setenv("CHATCLI_CONTEXT_WINDOW", "-1")
	assert.Equal(t, 50000, GetContextWindow("UNKNOWN_PROVIDER", "x"))
}

// TestStackSpotCatalogEntry pins the StackSpot defaults. The agent API does
// not accept max_tokens, so these numbers only drive client-side bookkeeping
// (compaction budget, ctx% footer, /metrics) — but before this entry existed
// both lookups fell back to the generic 50K default and chat/agent history
// compaction fired far too early.
func TestStackSpotCatalogEntry(t *testing.T) {
	meta, ok := Resolve(ProviderStackSpot, "StackSpotAI")
	assert.True(t, ok, "default StackSpot model must resolve")
	assert.Equal(t, 128000, meta.ContextWindow)
	assert.Equal(t, 128000, meta.MaxOutputTokens)

	// Both lookups must agree with the entry, and an unknown StackSpot
	// variant must land on the raised provider fallbacks, not 50K.
	assert.Equal(t, 128000, GetContextWindow(ProviderStackSpot, "StackSpotAI"))
	assert.Equal(t, 128000, GetContextWindow(ProviderStackSpot, "some-future-model"))
	assert.Equal(t, 128000, GetMaxTokens(ProviderStackSpot, "StackSpotAI", 0))
	assert.Equal(t, 128000, GetMaxTokens(ProviderStackSpot, "some-future-model", 0))

	// Explicit override (e.g. /max-tokens or STACKSPOT_MAX_TOKENS) still
	// has the highest priority.
	assert.Equal(t, 9000, GetMaxTokens(ProviderStackSpot, "StackSpotAI", 9000))
}

func TestMoonshotCatalogEntries(t *testing.T) {
	// Kimi K3 (flagship, Jul 2026): 1M window via Kimi Delta Attention,
	// output at the K2.x 128K cap. Alias "k3" must land on this entry and
	// the dotted K2 ids must not shadow it.
	k3, ok := Resolve(ProviderMoonshot, "kimi-k3")
	assert.True(t, ok, "kimi-k3 must resolve")
	assert.Equal(t, 1048576, k3.ContextWindow)
	assert.Equal(t, 131072, k3.MaxOutputTokens)
	assert.Contains(t, k3.Capabilities, "vision")
	assert.Contains(t, k3.Capabilities, "thinking")
	short, ok := Resolve(ProviderMoonshot, "k3")
	assert.True(t, ok)
	assert.Equal(t, "kimi-k3", short.ID)

	// Kimi K2.7 Code line (Jun 2026): 256K context, 32,768 default output
	// per platform.kimi.ai/docs/pricing. The highspeed entry sits BEFORE
	// the plain -code entry in the registry — Resolve matches aliases by
	// prefix/contains in registry order, so the plain entry's aliases
	// would otherwise swallow the highspeed id.
	hs, ok := Resolve(ProviderMoonshot, "kimi-k2.7-code-highspeed")
	assert.True(t, ok, "kimi-k2.7-code-highspeed must resolve")
	assert.Equal(t, "kimi-k2.7-code-highspeed", hs.ID, "highspeed must not be shadowed by kimi-k2.7-code")
	assert.Equal(t, 262144, hs.ContextWindow)
	assert.Equal(t, 32768, hs.MaxOutputTokens)

	code, ok := Resolve(ProviderMoonshot, "kimi-k2.7-code")
	assert.True(t, ok, "kimi-k2.7-code must resolve")
	assert.Equal(t, "kimi-k2.7-code", code.ID)
	assert.Equal(t, 262144, code.ContextWindow)
	assert.Equal(t, 32768, code.MaxOutputTokens)
	// The bare k2.7 spellings ride the -code entry via aliases.
	bare, ok := Resolve(ProviderMoonshot, "kimi-k2.7")
	assert.True(t, ok)
	assert.Equal(t, "kimi-k2.7-code", bare.ID)

	// Pin the public specs of the Kimi K2.6/K2.5 entries so silent drift on
	// the model card (e.g. catalog edits during a refactor) shows up here
	// instead of at runtime.
	for _, id := range []string{"kimi-k2.6", "kimi-k2.5", "kimi-latest"} {
		meta, ok := Resolve(ProviderMoonshot, id)
		assert.True(t, ok, "expected %s to resolve", id)
		assert.Equal(t, 262144, meta.ContextWindow, "%s context window", id)
		assert.Contains(t, meta.Capabilities, "tools", "%s should advertise tools", id)
		assert.Contains(t, meta.Capabilities, "thinking", "%s should advertise thinking", id)
	}

	// moonshot-v1-* family is the classic split — verify both ends.
	v18k, ok := Resolve(ProviderMoonshot, "moonshot-v1-8k")
	assert.True(t, ok)
	assert.Equal(t, 8192, v18k.ContextWindow)

	v1128k, ok := Resolve(ProviderMoonshot, "moonshot-v1-128k")
	assert.True(t, ok)
	assert.Equal(t, 131072, v1128k.ContextWindow)
}

func TestGetPreferredAPI(t *testing.T) {
	assert.Equal(t, APIResponses, GetPreferredAPI(ProviderOpenAI, "gpt-5"))
	assert.Equal(t, APIResponses, GetPreferredAPI(ProviderOpenAI, "gpt-5.5"))
	assert.Equal(t, APIResponses, GetPreferredAPI(ProviderOpenAI, "gpt-5.5-pro"))
	assert.Equal(t, APIChatCompletions, GetPreferredAPI(ProviderOpenAI, "gpt-4o"))
	assert.Equal(t, APIAnthropicMessages, GetPreferredAPI(ProviderClaudeAI, "claude-3-opus"))
	assert.Equal(t, APIAssistants, GetPreferredAPI(ProviderOpenAIAssistant, "gpt-4o")) // Provider específico
	assert.Equal(t, PreferredAPI("gemini_api"), GetPreferredAPI(ProviderGoogleAI, "gemini-2.5-pro"))
}

// TestClaudeOpus48Specs pins the May 28 2026 launch specs and the new
// capability flags the client uses to drive feature dispatch:
//   - adaptive_thinking → `thinking:{type:"adaptive"}` (no budget_tokens)
//   - fast_mode → `speed:"fast"` research preview
//   - mid_conversation_system → role:"system" allowed mid-conversation
//   - low_cache_minimum → 1,024-token cache floor
//
// TestClaudeFable5Specs pins the published Fable 5 specs: 1M context,
// 128K max output, adaptive-thinking-only request surface. Fable 5 is the
// tier above Opus; the "fable"/"fable-5" shorthands must resolve to it.
// fast_mode must NOT be advertised — the speed parameter is not documented
// for Fable 5, and advertising it would make the claudeai client emit
// `speed:"fast"` on a model that may reject it.
func TestClaudeFable5Specs(t *testing.T) {
	for _, id := range []string{"claude-fable-5", "fable-5", "fable"} {
		meta, ok := Resolve(ProviderClaudeAI, id)
		assert.True(t, ok, "expected %s to resolve on ProviderClaudeAI", id)
		assert.Equal(t, "claude-fable-5", meta.ID, "alias %s must resolve to claude-fable-5", id)
		assert.Equal(t, 1000000, meta.ContextWindow, "Fable 5 context window is 1M tokens")
		assert.Equal(t, 128000, meta.MaxOutputTokens, "Fable 5 max output is 128K")
		assert.Equal(t, APIAnthropicMessages, meta.PreferredAPI)
	}

	for _, capability := range []string{
		"tools", "json_mode", "adaptive_thinking", "mid_conversation_system",
	} {
		assert.True(t,
			HasCapability(ProviderClaudeAI, "claude-fable-5", capability),
			"claude-fable-5 should advertise %q capability", capability)
	}
	assert.False(t,
		HasCapability(ProviderClaudeAI, "claude-fable-5", "fast_mode"),
		"claude-fable-5 must not advertise fast_mode (speed param undocumented)")
}

// Drift in these advertised capabilities is a request-shape change that
// the client cares about, so we pin them here.
func TestClaudeOpus48Specs(t *testing.T) {
	meta, ok := Resolve(ProviderClaudeAI, "claude-opus-4-8")
	assert.True(t, ok, "claude-opus-4-8 must resolve on ProviderClaudeAI")
	assert.Equal(t, 1000000, meta.ContextWindow, "Opus 4.8 default context window is 1M tokens")
	assert.Equal(t, 128000, meta.MaxOutputTokens, "Opus 4.8 max output is 128K")
	assert.Equal(t, APIAnthropicMessages, meta.PreferredAPI)

	for _, capability := range []string{
		"tools", "adaptive_thinking", "fast_mode",
		"mid_conversation_system", "low_cache_minimum",
	} {
		assert.True(t,
			HasCapability(ProviderClaudeAI, "claude-opus-4-8", capability),
			"claude-opus-4-8 should advertise %q capability", capability)
	}

	// Bedrock mirror — canonical is the global. inference profile over the
	// dateless id (Opus 4.8 has no ARN-versioned model ID on Bedrock and
	// the bare id is not on-demand invokable).
	bedMeta, ok := Resolve(ProviderBedrock, "claude-opus-4-8")
	assert.True(t, ok, "claude-opus-4-8 must resolve via Bedrock alias")
	assert.Equal(t, "global.anthropic.claude-opus-4-8", bedMeta.ID)
	assert.Equal(t, 1000000, bedMeta.ContextWindow)
	assert.Equal(t, 128000, bedMeta.MaxOutputTokens)
	assert.True(t,
		HasCapability(ProviderBedrock, "claude-opus-4-8", "adaptive_thinking"),
		"Bedrock Opus 4.8 mirror should advertise adaptive_thinking")
	// The previously-shipped (fabricated) dated profile ID must keep
	// resolving for anyone who pinned it in scripts or env.
	legacy, ok := Resolve(ProviderBedrock, "global.anthropic.claude-opus-4-8-20260528-v1:0")
	assert.True(t, ok, "legacy dated Bedrock id must still resolve as alias")
	assert.Equal(t, "global.anthropic.claude-opus-4-8", legacy.ID)
	// fast_mode is a first-party research preview and
	// mid_conversation_system is not served by Bedrock — neither may be
	// advertised on the Bedrock mirror or the client would emit
	// parameters AWS rejects.
	assert.False(t, HasCapability(ProviderBedrock, "claude-opus-4-8", "fast_mode"))
	assert.False(t, HasCapability(ProviderBedrock, "claude-opus-4-8", "mid_conversation_system"))
}

// TestClaudeOpus5Specs pins the published Opus 5 specs (models overview,
// Jul 2026): 1M context, 128K max output, adaptive thinking, $5/$25 tier.
// "opus-5" shorthand must resolve to it and must NOT be swallowed by any
// opus-4.x alias (nor by 4.0's loose "opus-4" prefix).
func TestClaudeOpus5Specs(t *testing.T) {
	for _, id := range []string{"claude-opus-5", "opus-5"} {
		meta, ok := Resolve(ProviderClaudeAI, id)
		assert.True(t, ok, "expected %s to resolve on ProviderClaudeAI", id)
		assert.Equal(t, "claude-opus-5", meta.ID, "alias %s must resolve to claude-opus-5", id)
		assert.Equal(t, 1000000, meta.ContextWindow, "Opus 5 context window is 1M tokens")
		assert.Equal(t, 128000, meta.MaxOutputTokens, "Opus 5 max output is 128K")
		assert.Equal(t, APIAnthropicMessages, meta.PreferredAPI)
	}
	for _, capability := range []string{"tools", "json_mode", "vision", "adaptive_thinking"} {
		assert.True(t,
			HasCapability(ProviderClaudeAI, "claude-opus-5", capability),
			"claude-opus-5 should advertise %q capability", capability)
	}
	assert.False(t,
		HasCapability(ProviderClaudeAI, "claude-opus-5", "fast_mode"),
		"claude-opus-5 must not advertise fast_mode (not documented for Opus 5)")
	assert.False(t,
		HasCapability(ProviderClaudeAI, "claude-opus-5", "mid_conversation_system"),
		"mid_conversation_system is documented for Opus 4.8 only")
	// Regression guards: the opus-4.x lookups must keep resolving to their
	// own entries after the opus-5 entry lands, and vice-versa.
	m48, ok := Resolve(ProviderClaudeAI, "claude-opus-4-8")
	assert.True(t, ok)
	assert.Equal(t, "claude-opus-4-8", m48.ID)
	m40, ok := Resolve(ProviderClaudeAI, "opus-4")
	assert.True(t, ok)
	assert.NotEqual(t, "claude-opus-5", m40.ID, "generic opus-4 alias must not land on Opus 5")
}

// TestClaudeSonnet5Specs pins the published Sonnet 5 specs (models
// overview, Jun 2026): 1M context, 128K max output, adaptive thinking,
// $3/$15 tier. "sonnet-5" shorthand must resolve to it and must NOT be
// swallowed by any sonnet-4.x alias.
func TestClaudeSonnet5Specs(t *testing.T) {
	for _, id := range []string{"claude-sonnet-5", "sonnet-5"} {
		meta, ok := Resolve(ProviderClaudeAI, id)
		assert.True(t, ok, "expected %s to resolve on ProviderClaudeAI", id)
		assert.Equal(t, "claude-sonnet-5", meta.ID, "alias %s must resolve to claude-sonnet-5", id)
		assert.Equal(t, 1000000, meta.ContextWindow, "Sonnet 5 context window is 1M tokens")
		assert.Equal(t, 128000, meta.MaxOutputTokens, "Sonnet 5 max output is 128K")
		assert.Equal(t, APIAnthropicMessages, meta.PreferredAPI)
	}
	for _, capability := range []string{"tools", "json_mode", "vision", "adaptive_thinking"} {
		assert.True(t,
			HasCapability(ProviderClaudeAI, "claude-sonnet-5", capability),
			"claude-sonnet-5 should advertise %q capability", capability)
	}
	assert.False(t,
		HasCapability(ProviderClaudeAI, "claude-sonnet-5", "fast_mode"),
		"claude-sonnet-5 must not advertise fast_mode (Opus-tier research preview only)")
	// Regression guard: sonnet-4-x lookups must keep resolving to their own
	// entries after the sonnet-5 entry lands.
	m46, ok := Resolve(ProviderClaudeAI, "claude-sonnet-4-6")
	assert.True(t, ok)
	assert.Equal(t, "claude-sonnet-4-6", m46.ID)
}

// TestBedrockFable5Entry pins Fable 5 on Bedrock: dateless
// anthropic.claude-fable-5 id (reachable through InvokeModel per the
// Bedrock docs), 1M context so auto-compaction doesn't fire on the
// 50K unknown-model default, and adaptive_thinking so effort routing
// emits `thinking:{type:"adaptive"}` instead of a budgeted block.
func TestBedrockFable5Entry(t *testing.T) {
	for _, id := range []string{
		"anthropic.claude-fable-5",
		"global.anthropic.claude-fable-5",
		"claude-fable-5",
		"bedrock-fable-5",
	} {
		meta, ok := Resolve(ProviderBedrock, id)
		assert.True(t, ok, "expected %s to resolve on ProviderBedrock", id)
		assert.Equal(t, "anthropic.claude-fable-5", meta.ID, "alias %s must resolve to the Bedrock Fable 5 entry", id)
		assert.Equal(t, 1000000, meta.ContextWindow)
		assert.Equal(t, 128000, meta.MaxOutputTokens)
		assert.Equal(t, APIAnthropicMessages, meta.PreferredAPI)
	}
	assert.Equal(t, 1000000, GetContextWindow(ProviderBedrock, "anthropic.claude-fable-5"))
	assert.True(t,
		HasCapability(ProviderBedrock, "anthropic.claude-fable-5", "adaptive_thinking"),
		"Bedrock Fable 5 must advertise adaptive_thinking")
	assert.False(t,
		HasCapability(ProviderBedrock, "anthropic.claude-fable-5", "fast_mode"))
	assert.False(t,
		HasCapability(ProviderBedrock, "anthropic.claude-fable-5", "mid_conversation_system"))
}

// TestBedrockOpus5Entry pins Opus 5 on Bedrock. Like Sonnet 5 and Fable 5
// it is served through the Claude-in-Amazon-Bedrock Messages endpoint,
// advertised via bedrock_mantle_only so the client routes it to the
// Mantle wire instead of InvokeModel.
func TestBedrockOpus5Entry(t *testing.T) {
	for _, id := range []string{
		"anthropic.claude-opus-5",
		"global.anthropic.claude-opus-5",
		"claude-opus-5",
		"bedrock-opus-5",
	} {
		meta, ok := Resolve(ProviderBedrock, id)
		assert.True(t, ok, "expected %s to resolve on ProviderBedrock", id)
		assert.Equal(t, "anthropic.claude-opus-5", meta.ID, "alias %s must resolve to the Bedrock Opus 5 entry", id)
		assert.Equal(t, 1000000, meta.ContextWindow)
		assert.Equal(t, 128000, meta.MaxOutputTokens)
		assert.Equal(t, APIAnthropicMessages, meta.PreferredAPI)
	}
	assert.True(t,
		HasCapability(ProviderBedrock, "anthropic.claude-opus-5", "adaptive_thinking"))
	assert.True(t,
		HasCapability(ProviderBedrock, "anthropic.claude-opus-5", "bedrock_mantle_only"),
		"Opus 5 must be flagged mantle-only so the client picks the Messages endpoint")
	assert.False(t,
		HasCapability(ProviderBedrock, "anthropic.claude-opus-5", "fast_mode"))
	assert.False(t,
		HasCapability(ProviderBedrock, "anthropic.claude-opus-5", "mid_conversation_system"))
	// Regression guard: the Bedrock opus-4.x entries keep resolving to
	// their own IDs.
	m48, ok := Resolve(ProviderBedrock, "anthropic.claude-opus-4-8")
	assert.True(t, ok)
	assert.Equal(t, "global.anthropic.claude-opus-4-8", m48.ID)
}

// TestOpenRouterAnthropic5Family pins the OpenRouter static defaults for
// the Anthropic 5-family slugs. These entries exist so context-window
// sizing is correct (1M, not the 50K unknown-model fallback) even before
// the dynamic ListModels catalog loads.
func TestOpenRouterAnthropic5Family(t *testing.T) {
	for _, id := range []string{
		"anthropic/claude-opus-5",
		"anthropic/claude-sonnet-5",
		"anthropic/claude-fable-5",
	} {
		meta, ok := Resolve(ProviderOpenRouter, id)
		assert.True(t, ok, "expected %s to resolve on ProviderOpenRouter", id)
		assert.Equal(t, id, meta.ID)
		assert.Equal(t, 1000000, meta.ContextWindow, "%s context window is 1M on OpenRouter", id)
		assert.Equal(t, 128000, meta.MaxOutputTokens)
	}
}

// TestBedrockSonnet5Entry pins Sonnet 5 on Bedrock. The model is served
// exclusively by the bedrock-mantle Messages endpoint (no InvokeModel
// surface), which the entry advertises through the bedrock_mantle_only
// capability — the client uses it to route the request to the right wire.
func TestBedrockSonnet5Entry(t *testing.T) {
	for _, id := range []string{
		"anthropic.claude-sonnet-5",
		"global.anthropic.claude-sonnet-5",
		"claude-sonnet-5",
		"bedrock-sonnet-5",
	} {
		meta, ok := Resolve(ProviderBedrock, id)
		assert.True(t, ok, "expected %s to resolve on ProviderBedrock", id)
		assert.Equal(t, "anthropic.claude-sonnet-5", meta.ID, "alias %s must resolve to the Bedrock Sonnet 5 entry", id)
		assert.Equal(t, 1000000, meta.ContextWindow)
		assert.Equal(t, 128000, meta.MaxOutputTokens)
		assert.Equal(t, APIAnthropicMessages, meta.PreferredAPI)
	}
	assert.True(t,
		HasCapability(ProviderBedrock, "anthropic.claude-sonnet-5", "adaptive_thinking"))
	assert.True(t,
		HasCapability(ProviderBedrock, "anthropic.claude-sonnet-5", "bedrock_mantle_only"),
		"Sonnet 5 must be flagged mantle-only so the client picks the Messages endpoint")
	assert.False(t,
		HasCapability(ProviderBedrock, "anthropic.claude-sonnet-5", "fast_mode"))
	assert.False(t,
		HasCapability(ProviderBedrock, "anthropic.claude-sonnet-5", "mid_conversation_system"))
	// Regression guard: the 4.x Bedrock sonnets keep resolving to their
	// own entries.
	m46, ok := Resolve(ProviderBedrock, "claude-sonnet-4-6")
	assert.True(t, ok)
	assert.Equal(t, "global.anthropic.claude-sonnet-4-6", m46.ID)
}

// TestGLM52Entry pins GLM-5.2 (Z.AI, Jun 13 2026): 1M-token context and
// 128K max output per docs.z.ai/guides/llm/glm-5.2. The entry must sit
// ahead of the glm-5 / glm-5.1 entries so the more specific id is never
// shadowed by their alias prefixes.
func TestGLM52Entry(t *testing.T) {
	for _, id := range []string{"glm-5.2", "glm-5-2"} {
		meta, ok := Resolve(ProviderZAI, id)
		assert.True(t, ok, "expected %s to resolve on ProviderZAI", id)
		assert.Equal(t, "glm-5.2", meta.ID, "alias %s must resolve to glm-5.2", id)
		assert.Equal(t, 1000000, meta.ContextWindow, "GLM-5.2 context window is 1M tokens")
		assert.Equal(t, 128000, meta.MaxOutputTokens, "GLM-5.2 max output is 128K")
		assert.Equal(t, APIChatCompletions, meta.PreferredAPI)
	}
	assert.Equal(t, 1000000, GetContextWindow(ProviderZAI, "glm-5.2"))
	// glm-5 must keep resolving to its own entry (no shadowing either way).
	m5, ok := Resolve(ProviderZAI, "glm-5")
	assert.True(t, ok)
	assert.Equal(t, "glm-5", m5.ID)
}

// TestGPT55LimitsAndCapabilities pins the published Apr 23 2026 specs:
// 1,050,000-token context window and 128,000 max output for both the
// base and pro variants. If OpenAI revises these limits, the failure
// here is the signal to update the registry entries (and the doc note
// next to them) rather than silently drifting.
func TestGPT55LimitsAndCapabilities(t *testing.T) {
	for _, id := range []string{"gpt-5.5", "gpt-5.5-pro"} {
		meta, ok := Resolve(ProviderOpenAI, id)
		assert.True(t, ok, "expected %s to resolve", id)
		assert.Equal(t, 1050000, meta.ContextWindow, "%s context window", id)
		assert.Equal(t, 128000, meta.MaxOutputTokens, "%s max output", id)
		assert.Equal(t, APIResponses, meta.PreferredAPI, "%s preferred API", id)
		assert.Contains(t, meta.Capabilities, "tools", "%s should advertise tools", id)
		assert.Contains(t, meta.Capabilities, "json_mode", "%s should advertise json_mode", id)
		assert.Contains(t, meta.Capabilities, "vision", "%s should advertise vision", id)
	}

	// Catalog max-tokens lookup honors the explicit entry value, not the
	// generic gpt-5 fallback (50000) defined in GetMaxTokens.
	assert.Equal(t, 128000, GetMaxTokens(ProviderOpenAI, "gpt-5.5", 0))
	assert.Equal(t, 128000, GetMaxTokens(ProviderOpenAI, "gpt-5.5-pro", 0))
}

// gpt-5.6 (Jul 2026): Sol/Terra/Luna compartilham 1.05M de contexto e 128K
// de output na API de plataforma. O ponto crítico pinado aqui é que os IDs
// exatos resolvem para as SUAS entradas — antes desta família entrar no
// catálogo, "gpt-5.6-*" caía por prefixo na entrada gpt-5 (400K de contexto).
func TestGPT56FamilyEntries(t *testing.T) {
	for _, id := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		meta, ok := Resolve(ProviderOpenAI, id)
		assert.True(t, ok, "expected %s to resolve", id)
		assert.Equal(t, id, meta.ID, "%s must resolve to its own entry, not a prefix fallback", id)
		assert.Equal(t, 1050000, meta.ContextWindow, "%s context window", id)
		assert.Equal(t, 128000, meta.MaxOutputTokens, "%s max output", id)
		assert.Equal(t, APIResponses, meta.PreferredAPI, "%s preferred API", id)
		assert.Contains(t, meta.Capabilities, "tools", "%s should advertise tools", id)
		assert.Contains(t, meta.Capabilities, "json_mode", "%s should advertise json_mode", id)
		assert.Contains(t, meta.Capabilities, "vision", "%s should advertise vision", id)
		assert.Equal(t, 128000, GetMaxTokens(ProviderOpenAI, id, 0), "%s max tokens lookup", id)
	}

	// Alias genérico da família aponta para o flagship (Sol) sem sombrear
	// os IDs exatos dos outros tiers.
	meta, ok := Resolve(ProviderOpenAI, "gpt-5.6")
	assert.True(t, ok)
	assert.Equal(t, "gpt-5.6-sol", meta.ID)
}

// DEVIN provider (Devin CLI wrapper): the catalog mirrors the models served
// by the enterprise CLI. Pins the two things that matter: exact IDs resolve
// to their own entries (Devin slugs use dots, so "claude-sonnet-4" is a
// prefix of "claude-sonnet-4.5" — tier-1 exact match must win), and the
// provider filter keeps DEVIN entries from shadowing other providers.
func TestDevinCatalogEntries(t *testing.T) {
	for id, wantCtx := range map[string]int{
		"claude-opus-5":     1000000,
		"claude-sonnet-5":   1000000,
		"claude-sonnet-4.6": 1000000,
		"claude-sonnet-4.5": 200000,
		"claude-sonnet-4":   200000,
		"gpt-5.6-luna":      1050000,
		"gpt-5.1":           400000,
		"gpt-4.1":           1047576,
		"gemini-3.6-flash":  1048576,
		"swe-1.7-lightning": 200000,
		"kimi-k3":           1048576,
		"kimi-k2.7":         262144,
		"deepseek-v4-pro":   1000000,
	} {
		meta, ok := Resolve(ProviderDevin, id)
		assert.True(t, ok, "expected %s to resolve for DEVIN", id)
		assert.Equal(t, id, meta.ID, "%s must resolve to its own entry", id)
		assert.Equal(t, ProviderDevin, meta.Provider, "%s provider", id)
		assert.Equal(t, wantCtx, meta.ContextWindow, "%s context window", id)
	}

	// Cross-provider isolation: the dotted Devin slug must NOT leak into
	// CLAUDEAI lookups, and the CLAUDEAI dashed ID must not hit DEVIN.
	meta, ok := Resolve(ProviderClaudeAI, "claude-sonnet-4-6")
	assert.True(t, ok)
	assert.Equal(t, ProviderClaudeAI, meta.Provider)

	// swe-1.5 left the enterprise CLI roster (Jul 2026): a non-exact
	// lookup must fall back to provider defaults, not a stale entry.
	_, ok = Resolve(ProviderDevin, "swe-1.5")
	assert.False(t, ok, "swe-1.5 was removed from the Devin roster")

	// Unknown model under DEVIN falls back to the conservative provider
	// defaults instead of the generic 50K that causes compaction storms.
	assert.Equal(t, 200000, GetContextWindow(ProviderDevin, "some-future-model"))
	assert.Equal(t, 32000, GetMaxTokens(ProviderDevin, "some-future-model", 0))
}

// TestBedrockProviderFallbacks pins the Bedrock provider fallbacks for models
// absent from the registry. Before these cases existed, any unresolvable
// Bedrock model (application inference profiles with opaque IDs, marketplace
// or freshly-launched models) fell through to the generic 50K default — the
// same bug class as StackSpot (PR #1044) — which shrank the agent-mode
// compaction budget to ~120K chars and made history compaction fire on
// nearly every turn.
func TestBedrockProviderFallbacks(t *testing.T) {
	// Opaque application-inference-profile ARN: nothing in the registry can
	// match it. Non-Claude current Bedrock models (Nova, Llama, Mistral,
	// DeepSeek) all sit at 128K — that is the safe generic floor.
	opaque := "arn:aws:bedrock:us-east-1:000000000000:application-inference-profile/synthetic0000"
	assert.Equal(t, 128000, GetContextWindow(ProviderBedrock, opaque))
	assert.Equal(t, 8192, GetMaxTokens(ProviderBedrock, opaque, 0))

	// Future Claude model not yet in the registry: the current family
	// (Fable 5, Sonnet 5, Opus 4.6+) is all 1M input; the older 200K
	// models have their own registry entries, so an unknown Claude is a
	// fresh launch. Output floor stays 64K (some current models cap there
	// and an over-cap max_tokens is a hard API error).
	assert.Equal(t, 1000000, GetContextWindow(ProviderBedrock, "anthropic.claude-future-model"))
	assert.Equal(t, 64000, GetMaxTokens(ProviderBedrock, "anthropic.claude-future-model", 0))

	// Explicit override still has the highest priority.
	assert.Equal(t, 9000, GetMaxTokens(ProviderBedrock, opaque, 9000))
}

// TestGemini3FamilyEntries pins the Gemini 3.x generation added Jul 2026.
// Every model page on ai.google.dev documents the uniform 1,048,576 input /
// 65,536 output profile; a missing entry would drop these ids onto the
// GoogleAI provider fallback and undersize the context window.
func TestGemini3FamilyEntries(t *testing.T) {
	for _, id := range []string{
		"gemini-3.7-flash",
		"gemini-3.6-flash",
		"gemini-3.5-flash",
		"gemini-3.5-flash-lite",
		"gemini-3.1-pro-preview",
		"gemini-3.1-flash-lite",
		"gemini-3-flash-preview",
	} {
		meta, ok := Resolve(ProviderGoogleAI, id)
		if !assert.True(t, ok, "missing catalog entry for %s", id) {
			continue
		}
		assert.Equal(t, id, meta.ID, "id %s must resolve to its own entry, not an older generation", id)
		assert.Equal(t, 1048576, meta.ContextWindow, "%s", id)
		assert.Equal(t, 65536, meta.MaxOutputTokens, "%s", id)
	}
	// The customtools variant and the bare 3.1-pro spelling ride the
	// preview entry via aliases.
	meta, ok := Resolve(ProviderGoogleAI, "gemini-3.1-pro-preview-customtools")
	assert.True(t, ok)
	assert.Equal(t, "gemini-3.1-pro-preview", meta.ID)
}

// TestGrok2026Entries pins the 2026 xAI lineup (context windows per
// docs.x.ai; xAI documents no output cap, so entries keep the repo's 16K
// ceiling convention).
func TestGrok2026Entries(t *testing.T) {
	cases := map[string]int{
		"grok-4.6":                     500000,
		"grok-4.5":                     500000,
		"grok-4.3":                     1000000,
		"grok-4.20-0309-reasoning":     1000000,
		"grok-4.20-0309-non-reasoning": 1000000,
		"grok-4.20-multi-agent-0309":   1000000,
		"grok-build-0.1":               256000,
	}
	for id, wantCtx := range cases {
		meta, ok := Resolve(ProviderXAI, id)
		if !assert.True(t, ok, "missing catalog entry for %s", id) {
			continue
		}
		assert.Equal(t, id, meta.ID, "id %s must resolve to its own entry", id)
		assert.Equal(t, wantCtx, meta.ContextWindow, "%s", id)
		assert.Equal(t, 16384, meta.MaxOutputTokens, "%s", id)
	}
	// The generic alias rides the reasoning variant and must not shadow
	// the more specific 4.20 ids (they sit earlier in the registry).
	meta, ok := Resolve(ProviderXAI, "grok-4.20")
	assert.True(t, ok)
	assert.Equal(t, "grok-4.20-0309-reasoning", meta.ID)
}

// TestBedrockFallbackMaxTokensFamilies pins the per-family output ceilings
// for Bedrock models outside the registry. Overshoot is a hard
// ValidationException, so each sniff sits on the SMALLEST ceiling among the
// family's current models; families whose real cap is below the generic
// 8192 keep their own floor.
func TestBedrockFallbackMaxTokensFamilies(t *testing.T) {
	cases := map[string]int{
		"anthropic.claude-hypothetical":        64000,
		"us.deepseek.v4-hypothetical-v1:0":     32768,
		"openai.gpt-oss-hypothetical-v1:0":     16384,
		"us.amazon.nova-hypothetical-v1:0":     5120,
		"amazon.titan-text-premier-v1:0":       3072,
		"amazon.titan-text-express-v1:0":       8192,
		"amazon.titan-tg1-hypothetical":        4096,
		"cohere.command-hypothetical-v1:0":     4096,
		"ai21.jamba-hypothetical-v1:0":         4096,
		"meta.llama9-hypothetical-v1:0":        8192,
		"mistral.mistral-hypothetical-v2:0":    8192,
		"qwen.qwen9-hypothetical-v1:0":         8192,
		"writer.palmyra-hypothetical-x99-v1:0": 8192,
	}
	for model, want := range cases {
		assert.Equal(t, want, bedrockFallbackMaxTokens(model), "model %s", model)
	}
}

// TestOpenAIAssistantContextWindowFallback: assistant mode reports provider
// OPENAI_ASSISTANT; without its own case it fell into the generic 50K default.
func TestOpenAIAssistantContextWindowFallback(t *testing.T) {
	assert.Equal(t, 128000, GetContextWindow(ProviderOpenAIAssistant, "unknown-assistant-model"))
}

// TestBedrockNovaEntries pins the Amazon Nova family. These are the flagship
// non-Anthropic Bedrock models; without registry entries their 300K/1M
// windows collapsed onto the provider fallback.
func TestBedrockNovaEntries(t *testing.T) {
	for model, wantCtx := range map[string]int{
		"amazon.nova-micro-v1:0":      128000,
		"amazon.nova-lite-v1:0":       300000,
		"amazon.nova-pro-v1:0":        300000,
		"amazon.nova-premier-v1:0":    1000000,
		"us.amazon.nova-pro-v1:0":     300000, // cross-region inference profile spelling
		"us.amazon.nova-premier-v1:0": 1000000,
	} {
		meta, ok := Resolve(ProviderBedrock, model)
		assert.True(t, ok, "expected %s to resolve for BEDROCK", model)
		assert.Equal(t, ProviderBedrock, meta.Provider, "%s provider", model)
		assert.Equal(t, wantCtx, meta.ContextWindow, "%s context window", model)
		assert.Equal(t, wantCtx, GetContextWindow(ProviderBedrock, model), "%s GetContextWindow", model)
	}
}

// TestBedrockFamily5ProfileAliases pins the geo/global inference-profile
// spellings AWS publishes for the family-5 models (model cards, Aug 2026):
// every profile id must resolve back to the canonical Mantle-servable
// entry so routing (bedrock_mantle_only) and specs stay consistent
// whatever spelling the operator configured.
func TestBedrockFamily5ProfileAliases(t *testing.T) {
	cases := map[string]string{
		"us.anthropic.claude-fable-5":      "anthropic.claude-fable-5",
		"global.anthropic.claude-fable-5":  "anthropic.claude-fable-5",
		"us.anthropic.claude-opus-5":       "anthropic.claude-opus-5",
		"eu.anthropic.claude-opus-5":       "anthropic.claude-opus-5",
		"au.anthropic.claude-opus-5":       "anthropic.claude-opus-5",
		"us.anthropic.claude-sonnet-5":     "anthropic.claude-sonnet-5",
		"eu.anthropic.claude-sonnet-5":     "anthropic.claude-sonnet-5",
		"au.anthropic.claude-sonnet-5":     "anthropic.claude-sonnet-5",
		"global.anthropic.claude-sonnet-5": "anthropic.claude-sonnet-5",
	}
	for id, want := range cases {
		meta, ok := Resolve(ProviderBedrock, id)
		if !assert.True(t, ok, "profile id %s must resolve", id) {
			continue
		}
		assert.Equal(t, want, meta.ID, "profile id %s", id)
		assert.Contains(t, meta.Capabilities, "bedrock_mantle_only", "profile id %s keeps Mantle routing", id)
	}
}
