package cli

import (
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/pricing"

	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetModelPricing pins the per-family pricing returned by the dispatcher.
// Each case is the same shape (provider, model, expected input, expected output)
// so adding a new family/model is a one-line addition.
func TestGetModelPricing(t *testing.T) {
	cases := []struct {
		name            string
		provider, model string
		wantIn, wantOut float64
	}{
		// Anthropic
		{"claude fable 5.1", "CLAUDEAI", "claude-fable-5-1", 10.0, 50.0},
		{"claude fable 5.1 bedrock id", "BEDROCK", "anthropic.claude-fable-5-1", 10.0, 50.0},
		{"claude fable 5.1 openrouter slug", "OPENROUTER", "anthropic/claude-fable-5.1", 10.0, 50.0},
		{"claude fable 5", "CLAUDEAI", "claude-fable-5", 10.0, 50.0},
		// Sonnet 5 kept its $2/$10 launch price permanently (Anthropic
		// cancelled the Sep 2026 increase) — the specific case must win
		// before the generic $3/$15 sonnet tier in every spelling.
		{"claude sonnet 5", "CLAUDEAI", "claude-sonnet-5", 2.0, 10.0},
		{"claude sonnet 5 bedrock id", "BEDROCK", "global.anthropic.claude-sonnet-5", 2.0, 10.0},
		{"claude sonnet 5 openrouter slug", "OPENROUTER", "anthropic/claude-sonnet-5", 2.0, 10.0},
		{"claude opus 4.8", "CLAUDEAI", "claude-opus-4-8", 5.0, 25.0},
		{"claude opus 4.7", "CLAUDEAI", "claude-opus-4-7", 5.0, 25.0},
		{"claude opus 4.6 bedrock id", "BEDROCK", "global.anthropic.claude-opus-4-6-20260115-v1:0", 5.0, 25.0},
		{"claude opus 4.5", "CLAUDEAI", "claude-opus-4-5", 5.0, 25.0},
		// Opus 5 keeps the $5/$25 tier in every spelling — the regression
		// trap is the generic "opus" case right below it at $15/$75.
		{"claude opus 5", "CLAUDEAI", "claude-opus-5", 5.0, 25.0},
		{"claude opus 5 bedrock id", "BEDROCK", "anthropic.claude-opus-5", 5.0, 25.0},
		{"claude opus 5 openrouter slug", "OPENROUTER", "anthropic/claude-opus-5", 5.0, 25.0},
		{"claude opus legacy", "CLAUDEAI", "claude-3-opus", 15.0, 75.0},
		{"claude opus 4.1 legacy", "CLAUDEAI", "claude-opus-4-1", 15.0, 75.0},
		{"claude sonnet", "CLAUDEAI", "claude-sonnet-4-6", 3.0, 15.0},
		{"claude haiku 4.5", "CLAUDEAI", "claude-haiku-4-5", 1.0, 5.0},
		{"claude haiku legacy", "CLAUDEAI", "claude-3-haiku", 0.25, 1.25},

		// OpenAI — specific before generic.
		// gpt-5.6 tiers (list prices after the Jul 30 2026 cuts: terra
		// −20%, luna −80%): the specific terra/luna tags must win before
		// the bare gpt-5.6 case, which covers the family alias that the
		// catalog resolves to Sol.
		{"gpt-5.6-terra before gpt-5.6", "OPENAI", "gpt-5.6-terra", 2.0, 12.0},
		{"gpt-5.6-luna before gpt-5.6", "OPENAI", "gpt-5.6-luna", 0.20, 1.20},
		// Sol dropped to $4/$20 on Aug 21 2026 (promotional "at least
		// through Nov 21 2026").
		{"gpt-5.6 generic is sol", "OPENAI", "gpt-5.6-sol", 4.0, 20.0},
		{"gpt-5.6 bare alias", "OPENAI", "gpt-5.6", 4.0, 20.0},
		{"gpt-5.6 sol bedrock id", "BEDROCK", "global.openai.gpt-5.6-sol", 4.0, 20.0},
		// gpt-5.5 … gpt-5: every id below 5.6 used to fall through to
		// "unknown" (zero). Pro/mini/nano before the base of each
		// generation.
		{"gpt-5.5-pro before gpt-5.5", "OPENAI", "gpt-5.5-pro", 30.0, 180.0},
		{"gpt-5.5", "OPENAI", "gpt-5.5", 5.0, 30.0},
		{"gpt-5.4-pro before gpt-5.4", "OPENAI", "gpt-5.4-pro", 30.0, 180.0},
		{"gpt-5.4-mini", "OPENAI", "gpt-5.4-mini", 0.75, 4.50},
		{"gpt-5.4-nano", "OPENAI", "gpt-5.4-nano", 0.20, 1.25},
		{"gpt-5.4", "OPENAI", "gpt-5.4", 2.50, 15.0},
		{"gpt-5.3-codex", "OPENAI", "gpt-5.3-codex", 1.75, 14.0},
		{"gpt-5.2-pro before gpt-5.2", "OPENAI", "gpt-5.2-pro", 21.0, 168.0},
		{"gpt-5.2", "OPENAI", "gpt-5.2", 1.75, 14.0},
		{"gpt-5-pro before gpt-5", "OPENAI", "gpt-5-pro", 15.0, 120.0},
		{"gpt-5-mini", "OPENAI", "gpt-5-mini", 0.25, 2.0},
		{"gpt-5-nano", "OPENAI", "gpt-5-nano", 0.05, 0.40},
		{"gpt-5.1 same tier as gpt-5", "OPENAI", "gpt-5.1", 1.25, 10.0},
		{"gpt-5", "OPENAI", "gpt-5", 1.25, 10.0},
		{"gpt-4o-mini before gpt-4o", "OPENAI", "gpt-4o-mini", 0.15, 0.60},
		{"gpt-4o", "OPENAI", "gpt-4o", 2.50, 10.0},
		{"gpt-4-turbo", "OPENAI", "gpt-4-turbo", 10.0, 30.0},
		{"gpt-4.1", "OPENAI", "gpt-4.1", 2.0, 8.0},
		{"gpt-4 bare", "OPENAI", "gpt-4", 30.0, 60.0},
		{"gpt-3.5", "OPENAI", "gpt-3.5-turbo", 0.50, 1.50},
		{"o3-mini", "OPENAI", "o3-mini", 1.10, 4.40},
		{"o4-mini", "OPENAI", "o4-mini", 1.10, 4.40},
		{"o3", "OPENAI", "o3", 10.0, 40.0},
		{"o1-mini before o1", "OPENAI", "o1-mini", 3.0, 12.0},
		{"o1", "OPENAI", "o1", 15.0, 60.0},

		// Google — Gemini 3.x (ai.google.dev pricing, Aug 2026). The
		// generic "gemini-3" (Pro) case is a substring of every 3.x id,
		// so the specific tags must win first; same for the flash-lite
		// vs flash pairs.
		{"gemini 3.7 flash", "GOOGLEAI", "gemini-3.7-flash", 0.75, 3.75},
		{"gemini 3.6 flash", "GOOGLEAI", "gemini-3.6-flash", 0.75, 3.75},
		{"gemini 3.5 flash-lite before flash", "GOOGLEAI", "gemini-3.5-flash-lite", 0.30, 2.50},
		{"gemini 3.5 flash", "GOOGLEAI", "gemini-3.5-flash", 1.50, 9.0},
		{"gemini 3.1 pro preview", "GOOGLEAI", "gemini-3.1-pro-preview", 2.0, 12.0},
		{"gemini 3.1 flash-lite", "GOOGLEAI", "gemini-3.1-flash-lite", 0.25, 1.50},
		{"gemini 3 flash preview", "GOOGLEAI", "gemini-3-flash-preview", 0.50, 3.0},
		{"gemini 3 pro generic last", "GOOGLEAI", "gemini-3-pro-preview", 2.0, 12.0},
		{"gemini 2.5 pro", "GOOGLEAI", "gemini-2.5-pro", 1.25, 10.0},
		{"gemini 2.5 flash-lite before flash", "GOOGLEAI", "gemini-2.5-flash-lite", 0.10, 0.40},
		{"gemini 2.5 flash", "GOOGLEAI", "gemini-2.5-flash", 0.30, 2.50},
		{"gemini 2.0", "GOOGLEAI", "gemini-2.0-flash", 0.075, 0.30},
		{"gemini 1.5 pro", "GOOGLEAI", "gemini-1.5-pro", 1.25, 5.0},
		{"gemini 1.5 flash", "GOOGLEAI", "gemini-1.5-flash", 0.075, 0.30},

		// xAI Grok — 2026 generation (docs.x.ai pricing, base <200K tier).
		{"grok-4.6", "XAI", "grok-4.6", 2.0, 6.0},
		{"grok-4.5", "XAI", "grok-4.5", 2.0, 6.0},
		{"grok-4.3", "XAI", "grok-4.3", 1.25, 2.50},
		{"grok-4.20 reasoning", "XAI", "grok-4.20-0309-reasoning", 1.25, 2.50},
		{"grok-build", "XAI", "grok-build-0.1", 1.0, 2.0},
		// Retired May 15 2026: xAI redirects these slugs to grok-4.3 and
		// bills grok-4.3 rates; grok-code-fast-1 is now grok-build-0.1.
		{"grok-3 redirected to 4.3 rates", "XAI", "grok-3", 1.25, 2.50},
		{"grok-4-fast redirected to 4.3 rates", "XAI", "grok-4-fast-reasoning", 1.25, 2.50},
		{"grok-code-fast-1 is grok-build", "XAI", "grok-code-fast-1", 1.0, 2.0},
		{"grok-4.6 on bedrock", "BEDROCK", "global.xai.grok-4.6", 2.0, 6.0},
		{"grok-2", "XAI", "grok-2", 2.0, 10.0},
		{"grok generic", "XAI", "grok-4", 5.0, 15.0},

		// DeepSeek — V4 (Aug 2026) uses the peak-hour list price (the
		// off-peak halves are not modeled); specific v4 tags before the
		// legacy r1/generic cases.
		{"deepseek-v4-pro", "OPENROUTER", "deepseek-v4-pro", 1.32, 3.96},
		{"deepseek-v4-flash", "OPENROUTER", "deepseek-v4-flash", 0.44, 1.32},
		{"deepseek-r1 before deepseek", "OPENROUTER", "deepseek-r1", 0.55, 2.19},
		{"deepseek bare", "OPENROUTER", "deepseek-chat", 0.27, 1.10},

		// Provider-keyed fallbacks
		{"minimax via model", "OTHER", "minimax-m2.7", 0.20, 1.10},
		{"minimax via provider", "MINIMAX", "anything", 0.20, 1.10},
		// Z.AI GLM-5 family — public list prices (docs.z.ai, Jun 2026):
		// GLM-5.2 $1.40/$4.40, GLM-5 $1.00/$3.20 per MTok. The specific
		// "glm-5.2" tag must win before the bare "glm-5" prefix; unknown
		// GLM/ZAI ids keep the conservative flat fallback.
		{"glm-5.3-flash before glm-5.3", "ZAI", "glm-5.3-flash", 0.15, 0.50},
		{"glm-5.3 same tier as 5.2", "ZAI", "glm-5.3", 1.40, 4.40},
		{"glm-5.2", "ZAI", "glm-5.2", 1.40, 4.40},
		{"glm-5.2 via model on other provider", "OTHER", "glm-5.2", 1.40, 4.40},
		{"glm-5.1 same tier as 5.2", "ZAI", "glm-5.1", 1.40, 4.40},
		{"glm-5-turbo own tier", "ZAI", "glm-5-turbo", 1.20, 4.00},
		{"glm-5v-turbo own tier", "ZAI", "glm-5v-turbo", 1.20, 4.00},
		{"glm-5 via glm model", "OTHER", "glm-5", 1.00, 3.20},
		{"zai via provider", "ZAI", "anything", 0.50, 0.50},
		// GLM-4.x list prices (docs.z.ai, Sep 2026): flash tiers are free,
		// flashx/air/airx/x have their own rows ahead of the family base.
		{"glm-4.7-flashx before glm-4.7-flash", "ZAI", "glm-4.7-flashx", 0.07, 0.40},
		{"glm-4.7-flash is free", "ZAI", "glm-4.7-flash", 0.0, 0.0},
		{"glm-4.5-airx before air", "ZAI", "glm-4.5-airx", 1.10, 4.50},
		{"glm-4.5-air", "ZAI", "glm-4.5-air", 0.20, 1.10},
		{"glm-4.5v vision tier", "ZAI", "glm-4.5v", 0.60, 1.80},
		{"glm-4.7 base", "ZAI", "glm-4.7", 0.60, 2.20},
		{"glm-4.5 base via other provider", "OTHER", "glm-4.5", 0.60, 2.20},

		// Moonshot (Kimi) — K3 has its own list price (well above the K2
		// line) and its specific case must win over the generic kimi match;
		// the K2.7 Code highspeed tier is 2× the K2.x standard and must
		// also win before the generic match. Everything else keeps the
		// conservative K2.6 single tier.
		{"kimi-k3 own tier", "MOONSHOT", "kimi-k3", 3.00, 15.00},
		{"kimi-k2.7-code-highspeed own tier", "MOONSHOT", "kimi-k2.7-code-highspeed", 1.90, 8.00},
		{"kimi-k2.7-code standard tier", "MOONSHOT", "kimi-k2.7-code", 0.95, 4.00},
		{"moonshot via provider", "MOONSHOT", "kimi-k2.6", 0.95, 4.00},
		// Retired ids (Aug 31 2026) keep the generic tier so a stale
		// session log still prices instead of reporting zero.
		{"kimi-k2.5 retired keeps generic tier", "MOONSHOT", "kimi-k2.5", 0.95, 4.00},
		{"moonshot-v1 retired keeps generic tier", "MOONSHOT", "moonshot-v1-128k", 0.95, 4.00},

		{"copilot", "COPILOT", "gpt-4o", 2.50, 10.0},
		{"ollama zero", "OLLAMA", "llama3", 0.0, 0.0},
		// Devin CLI: o binário não reporta tokens e o custo é da assinatura
		// Cognition — sempre zero, qualquer que seja o modelo roteado.
		{"devin zero", "DEVIN", "claude-sonnet-4.6", 0.0, 0.0},
		{"devin zero swe", "DEVIN", "swe-1.7-lightning", 0.0, 0.0},
		{"stackspot zero", "STACKSPOT", "stackspotai", 0.0, 0.0},

		// Unknown provider+model defaults to zero.
		{"unknown", "WHATEVER", "no-match", 0.0, 0.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out := getModelPricing(tc.provider, tc.model)
			assert.Equal(t, tc.wantIn, in, "input cost")
			assert.Equal(t, tc.wantOut, out, "output cost")
		})
	}
}

// TestGetModelPricing_CaseInsensitive ensures the dispatcher lowercases both
// inputs so the caller can pass capitalized provider/model names without
// silently falling through to the zero default.
func TestGetModelPricing_CaseInsensitive(t *testing.T) {
	in, out := getModelPricing("MOONSHOT", "Kimi-K2.6")
	assert.Equal(t, 0.95, in)
	assert.Equal(t, 4.0, out)

	in, out = getModelPricing("CLAUDEAI", "Claude-3-Opus")
	assert.Equal(t, 15.0, in)
	assert.Equal(t, 75.0, out)
}

// TestLookupModelPricing_ZAICodingPlan: com o plano ativo (toggle ou URL
// /coding/), tokens ZAI são da assinatura — tarifa zero com known=true,
// nunca "preço desconhecido". Sem o plano, a tabela normal volta a valer.
func TestLookupModelPricing_ZAICodingPlan(t *testing.T) {
	t.Setenv("ZAI_API_URL", "")
	t.Setenv("ZAI_USE_CODING_PLAN", "true")

	in, out, known := lookupModelPricing("ZAI", "glm-5.3")
	assert.True(t, known)
	assert.Zero(t, in)
	assert.Zero(t, out)

	// O curto-circuito é do provider ZAI: um glm servido via OPENAI
	// (gateway) continua na tabela normal.
	in, out, known = lookupModelPricing("OPENAI", "glm-5.3")
	assert.True(t, known)
	assert.Equal(t, 1.40, in)
	assert.Equal(t, 4.40, out)

	t.Setenv("ZAI_USE_CODING_PLAN", "")
	in, out, known = lookupModelPricing("ZAI", "glm-5.3")
	assert.True(t, known)
	assert.Equal(t, 1.40, in)
	assert.Equal(t, 4.40, out)
}

// Devin: the per-account rate the CLI listed wins over the zero default,
// exact id first, then the catalog family a variant or alias resolves to.
func TestLookupModelPricing_DevinListedRates(t *testing.T) {
	t.Cleanup(func() { pricing.ResetProvider(catalog.ProviderDevin) })

	in, out, known := lookupModelPricing("DEVIN", "claude-opus-5")
	assert.True(t, known)
	assert.Zero(t, in)
	assert.Zero(t, out)

	pricing.Register(catalog.ProviderDevin, "claude-opus-5", pricing.Rate{InputPerMTok: 5, OutputPerMTok: 25})
	in, out, known = lookupModelPricing("DEVIN", "claude-opus-5")
	assert.True(t, known)
	assert.Equal(t, 5.0, in)
	assert.Equal(t, 25.0, out)

	// Unlisted reasoning suffix resolves to the family through the catalog.
	in, _, _ = lookupModelPricing("DEVIN", "claude-opus-5-xhigh")
	assert.Equal(t, 5.0, in)

	// A routed claude-* id never falls through to the direct-API table.
	in, _, known = lookupModelPricing("DEVIN", "claude-sonnet-4.6")
	assert.True(t, known)
	assert.Zero(t, in)

	// The routing tier follows the listed rate instead of "unmetered".
	tier, _, _ := modelRoutingTier("DEVIN", "claude-opus-5")
	assert.Equal(t, "frontier", tier)
}
