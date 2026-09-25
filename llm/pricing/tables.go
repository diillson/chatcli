/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pricing

import (
	"strings"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/zai/codingplan"
)

// --- Pricing tables ---

// ListPrice returns the input and output list price of provider+model in
// USD per million tokens, plus a known flag: known=false means the model
// matched NO table entry — cost zero because the price is unknown, not
// because the backend is unmetered. Ollama/StackSpot/Copilot return
// known=true with zero prices (deliberately free from ChatCLI's viewpoint),
// and so does a Devin family no listing ever priced.
//
// Precedence: the CHATCLI_MODEL_PRICING override, then the per-account rate
// a provider listed at runtime (Register), then the static tables.
func ListPrice(provider, model string) (inputPerMTok, outputPerMTok float64, known bool) {
	return lookupModelPricing(provider, model)
}

// lookupModelPricing is ListPrice: dispatches to per-family helpers so each
// family can evolve independently without the function blowing past the
// project's cyclomatic budget. The helpers are tried in priority order:
// model-id heuristics first (model is authoritative for cloud providers),
// then provider-string fallbacks for wrappers and self-hosted backends.
func lookupModelPricing(provider, model string) (inputCost, outputCost float64, known bool) {
	model = strings.ToLower(model)
	provider = strings.ToLower(provider)

	// The operator's word outranks every source below: CHATCLI_MODEL_PRICING
	// pins a rate for any provider+model (or a provider-wide "*"), which is
	// how a metered wrapper that never tells ChatCLI its price — an
	// enterprise Devin CLI, a paid Ollama host, a subscription — still gets
	// a real cost line instead of a known zero.
	if in, out, ok := overriddenPricing(provider, model); ok {
		return in, out, true
	}

	// DEVIN antes das heurísticas de modelo: o wrapper roteia modelos com
	// nomes reconhecíveis (claude-*, gpt-*) mas a tarifa é a da conta
	// Cognition, não a da API direta — sem o curto-circuito,
	// claudePricing/openAIPricing cobrariam errado. A tarifa por modelo
	// vem da própria listagem do CLI (cost_summary) via Register; sem
	// ela (listagem sem cost_summary nos builds enterprise, CLI antigo,
	// sem login) vale a tabela estática espelhada da listagem de uma
	// conta; só uma família que nunca foi listada com preço fica em
	// zero-conhecido.
	if strings.Contains(provider, "devin") {
		if in, out, ok := devinListedPricing(model); ok {
			return in, out, true
		}
		if in, out, ok := devinStaticPricing(model); ok {
			return in, out, true
		}
		return 0, 0, true
	}

	// GLM Coding Plan: o endpoint /coding/ debita da assinatura, não dos
	// créditos por token — tarifa zero, como Devin/Ollama/StackSpot. Sem o
	// curto-circuito, zaiPricing cobraria como se fosse a API paga.
	if strings.Contains(provider, "zai") && codingplan.Active() {
		return 0, 0, true
	}
	// GitHub Copilot is a subscription: requests debit the plan's premium
	// request allowance, not a per-token invoice — before the model-name
	// heuristics, which would price gpt-*/claude-* at API rates.
	if strings.Contains(provider, "copilot") {
		return 0, 0, true
	}

	for _, fn := range []func(string) (float64, float64, bool){
		claudePricing,
		openAIPricing,
		googlePricing,
		grokPricing,
		deepseekPricing,
		zaiPricing,
		novaPricing,
	} {
		if in, out, ok := fn(model); ok {
			return in, out, true
		}
	}
	return providerFallbackPricing(provider, model)
}

func claudePricing(model string) (float64, float64, bool) {
	if !strings.Contains(model, "claude") {
		return 0, 0, false
	}
	switch {
	case strings.Contains(model, "fable"):
		// Fable 5: $10/$50 per MTok (tier above Opus).
		return 10.0, 50.0, true
	case strings.Contains(model, "opus-5-5"), strings.Contains(model, "opus-5.5"):
		// Opus 5.5 (Sep 22 2026): $4/$20 per MTok — the first Opus cheaper
		// than its predecessor (platform.claude.com/docs/en/about-claude/
		// pricing). Must match BEFORE "opus-5", which is its substring and
		// would bill it $5/$25. Both spellings cover the Bedrock
		// (anthropic.claude-opus-5-5) and OpenRouter (anthropic/
		// claude-opus-5.5) ids. Fast mode ($8/$40) is not modeled.
		return 4.0, 20.0, true
	case strings.Contains(model, "opus-5"):
		// Opus 5 (Jul 2026): keeps the $5/$25 Opus-tier price. Must match
		// BEFORE the generic "opus" case below, which is the $15/$75
		// legacy tier — the substring also covers the Bedrock
		// (anthropic.claude-opus-5) and OpenRouter (anthropic/claude-opus-5)
		// spellings.
		return 5.0, 25.0, true
	case strings.Contains(model, "opus-4-5"), strings.Contains(model, "opus-4-6"),
		strings.Contains(model, "opus-4-7"), strings.Contains(model, "opus-4-8"):
		// Opus 4.5 onward dropped to $5/$25 per MTok; the 1M context on
		// 4.6+ carries no long-context premium.
		return 5.0, 25.0, true
	case strings.Contains(model, "opus"):
		// Opus 3 / 4.0 / 4.1 legacy pricing.
		return 15.0, 75.0, true
	case strings.Contains(model, "sonnet-5"):
		// Sonnet 5: the $2/$10 launch rate became the permanent list price
		// (Anthropic cancelled the Sep 1 2026 increase). Must precede the
		// generic "sonnet" tier below; the substring also covers the
		// Bedrock (anthropic.claude-sonnet-5) and OpenRouter spellings.
		return 2.0, 10.0, true
	case strings.Contains(model, "sonnet"):
		return 3.0, 15.0, true
	case strings.Contains(model, "haiku-4-5"):
		return 1.0, 5.0, true
	case strings.Contains(model, "haiku"):
		return 0.25, 1.25, true
	}
	return 0, 0, false
}

// openAIPricing covers both the GPT-* and o-* reasoning families. Ordering
// matters: more specific tags ("gpt-4o-mini") must come before their parents
// ("gpt-4o").
func openAIPricing(model string) (float64, float64, bool) {
	switch {
	// gpt-6-astra (GA 03/Set/2026): $10/$50 no tier curto de contexto
	// (developers.openai.com/api/docs/pricing). Acima de 272K tokens de
	// input a OpenAI cobra $20/$75 sobre a request inteira — o mesmo
	// surcharge de long-context que a família 5.6 tem e que o
	// cost_tracker não modela (um único tier por modelo), então uma
	// sessão que estoure 272K é subestimada aqui. O caso cobre também o
	// id do Bedrock (openai.gpt-6-astra) e o slug do OpenRouter
	// (openai/gpt-6-astra), que repassam a mesma tarifa.
	case strings.Contains(model, "gpt-6-astra"):
		return 10.0, 50.0, true
	// gpt-6-sol / gpt-6-luna (GA 22/Set/2026, developers.openai.com/api/
	// docs/pricing): $2/$10 e $0,10/$0,50 no tier curto; acima de 272K de
	// input a request inteira dobra (mesmo surcharge da 5.6, não
	// modelado). Cobrem também os ids do Bedrock (openai.gpt-6-sol/-luna)
	// e os slugs do OpenRouter. Cache: coluna própria a 1,25x/10% — o
	// caso "gpt-6" do getCachePricing já pega os dois.
	case strings.Contains(model, "gpt-6-sol"):
		return 2.0, 10.0, true
	case strings.Contains(model, "gpt-6-luna"):
		return 0.10, 0.50, true
	// gpt-5.6 (Jul 2026): preços de lista da API por tier
	// (developers.openai.com/api/docs/pricing, Set/2026): terra e luna
	// desde o corte de 30/Jul; Sol caiu para $4/$20 em 21/Ago ("pelo
	// menos até 21/Nov/2026" — revisar na virada). O caso genérico
	// "gpt-5.6" cobre o alias de família (que o catálogo resolve para
	// Sol) e precisa vir depois dos tiers específicos. O surcharge de
	// long-context (>272K input = 2×/1,5×) não é modelado — cost_tracker
	// trabalha com um único tier por modelo. Os mesmos casos cobrem os
	// ids do Bedrock (global.openai.gpt-5.6-*), que a AWS cobra igual no
	// endpoint global.
	case strings.Contains(model, "gpt-5.6-terra"):
		return 2.0, 12.0, true
	case strings.Contains(model, "gpt-5.6-luna"):
		return 0.20, 1.20, true
	case strings.Contains(model, "gpt-5.6"):
		return 4.0, 20.0, true
	// gpt-5.5 … gpt-5 (developers.openai.com/api/docs/pricing, Set/2026).
	// Antes desta tabela todo id 5.x abaixo do 5.6 caía em "desconhecido"
	// e o /cost reportava zero. Pro/mini/nano específicos antes do tier
	// base de cada geração; "gpt-5.1" compartilha a tarifa do gpt-5.
	case strings.Contains(model, "gpt-5.5-pro"):
		return 30.0, 180.0, true
	case strings.Contains(model, "gpt-5.5"):
		return 5.0, 30.0, true
	case strings.Contains(model, "gpt-5.4-pro"):
		return 30.0, 180.0, true
	case strings.Contains(model, "gpt-5.4-mini"):
		return 0.75, 4.50, true
	case strings.Contains(model, "gpt-5.4-nano"):
		return 0.20, 1.25, true
	case strings.Contains(model, "gpt-5.4"):
		return 2.50, 15.0, true
	case strings.Contains(model, "gpt-5.3-codex"):
		return 1.75, 14.0, true
	case strings.Contains(model, "gpt-5.2-pro"):
		return 21.0, 168.0, true
	case strings.Contains(model, "gpt-5.2"):
		return 1.75, 14.0, true
	case strings.Contains(model, "gpt-5-pro"):
		return 15.0, 120.0, true
	case strings.Contains(model, "gpt-5-mini"):
		return 0.25, 2.0, true
	case strings.Contains(model, "gpt-5-nano"):
		return 0.05, 0.40, true
	case strings.Contains(model, "gpt-5.1"), strings.Contains(model, "gpt-5"):
		return 1.25, 10.0, true
	case strings.Contains(model, "gpt-4o-mini"):
		return 0.15, 0.60, true
	case strings.Contains(model, "gpt-4o"):
		return 2.50, 10.0, true
	case strings.Contains(model, "gpt-4-turbo"):
		return 10.0, 30.0, true
	case strings.Contains(model, "gpt-4.1"):
		return 2.0, 8.0, true
	case strings.Contains(model, "gpt-4"):
		return 30.0, 60.0, true
	case strings.Contains(model, "gpt-3.5"):
		return 0.50, 1.50, true
	case strings.Contains(model, "o3-mini"), strings.Contains(model, "o4-mini"):
		return 1.10, 4.40, true
	case strings.Contains(model, "o3"):
		return 10.0, 40.0, true
	case strings.Contains(model, "o1-mini"):
		return 3.0, 12.0, true
	case strings.Contains(model, "o1"):
		return 15.0, 60.0, true
	}
	return 0, 0, false
}

// googlePricing cobre as gerações Gemini 3.x/2.x/1.5 (ai.google.dev
// pricing, Aug 2026). Ordering: tags específicas antes das genéricas —
// "gemini-3" (Pro/preview) por último no bloco 3.x porque é substring de
// todos os ids 3.x; "gemini-2.5-flash-lite" antes de "gemini-2.5-flash".
// 3.8/3.7/3.6-flash usam o preço introdutório vigente ($0.75/$3.75 até
// 31/Dez/2026; dobra a partir de Jan/2027 — revisar na virada).
func googlePricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "gemini-3.8-flash"), strings.Contains(model, "gemini-3.7-flash"),
		strings.Contains(model, "gemini-3.6-flash"):
		// 3.8-flash (GA 02/Set/2026) entrou no mesmo tier introdutório.
		return 0.75, 3.75, true
	case strings.Contains(model, "gemini-3.5-flash-lite"):
		return 0.30, 2.50, true
	case strings.Contains(model, "gemini-3.5-flash"):
		return 1.50, 9.0, true
	case strings.Contains(model, "gemini-3.1-pro"):
		return 2.0, 12.0, true
	case strings.Contains(model, "gemini-3.1-flash-lite"):
		return 0.25, 1.50, true
	case strings.Contains(model, "gemini-3-flash"):
		return 0.50, 3.0, true
	case strings.Contains(model, "gemini-3"):
		// gemini-3 / gemini-3-pro(-preview) — mesmo tier do 3.1 Pro.
		return 2.0, 12.0, true
	case strings.Contains(model, "gemini-2.5-pro"):
		return 1.25, 10.0, true
	case strings.Contains(model, "gemini-2.5-flash-lite"):
		return 0.10, 0.40, true
	case strings.Contains(model, "gemini-2.5-flash"):
		return 0.30, 2.50, true
	case strings.Contains(model, "gemini-2.0"):
		return 0.075, 0.30, true
	case strings.Contains(model, "gemini-1.5-pro"):
		return 1.25, 5.0, true
	case strings.Contains(model, "gemini-1.5-flash"):
		return 0.075, 0.30, true
	}
	return 0, 0, false
}

// grokPricing cobre a geração 2026 (docs.x.ai/docs/pricing, Aug 2026).
// A xAI cobra por tier de prompt (<200K / ≥200K); cost_tracker modela um
// único tier, então usamos o tier base (<200K) — consistente com o
// tratamento de long-context da OpenAI acima. Específicos antes do
// genérico "grok".
func grokPricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "grok-4.7"), strings.Contains(model, "grok-4.6"), strings.Contains(model, "grok-4.5"):
		// grok-4.7 (21/Set/2026) mantém o tier $2/$6 (<200K de prompt;
		// acima disso a xAI dobra, surcharge não modelado).
		return 2.0, 6.0, true
	case strings.Contains(model, "grok-4.3"), strings.Contains(model, "grok-4.20"):
		return 1.25, 2.50, true
	case strings.Contains(model, "grok-build"), strings.Contains(model, "grok-code-fast"):
		// grok-code-fast-1 became an alias of grok-build-0.1 on May 15 2026.
		return 1.0, 2.0, true
	case strings.Contains(model, "grok-3"), strings.Contains(model, "grok-4-"):
		// Retired May 15 2026 (grok-3, grok-3-mini, grok-4-0709,
		// grok-4-fast-*, grok-4-1-fast-*): xAI keeps the slugs alive but
		// redirects them to grok-4.3 and bills grok-4.3 rates
		// (docs.x.ai/developers/migration/may-15-retirement).
		return 1.25, 2.50, true
	case strings.Contains(model, "grok-2"):
		return 2.0, 10.0, true
	case strings.Contains(model, "grok"):
		return 5.0, 15.0, true
	}
	return 0, 0, false
}

// zaiPricing covers Z.AI's GLM-5 family. Public list prices (docs.z.ai,
// Aug 2026): GLM-5.3/GLM-5.2 $1.40/$4.40, GLM-5.3-Flash $0.15/$0.50
// (list price; launch promo até 09/set/2026 não é refletida para não
// sub-reportar), GLM-5 $1.00/$3.20 per MTok. Ordering matters: the
// "-flash" tag must win before "glm-5.3", and the specific "glm-5.x"
// tags before the bare "glm-5" prefix. GLM-4.x and other Z.AI ids fall
// through to the conservative flat rate in providerFallbackPricing.
func zaiPricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "glm-5.3-flashx"), strings.Contains(model, "glm-5-3-flashx"):
		// GLM-5.3-FlashX (18/Set/2026): tier de alta velocidade da Flash,
		// $0.37/$1.25 — antes da Flash, que é prefixo do id.
		return 0.37, 1.25, true
	case strings.Contains(model, "glm-5.3-flash"), strings.Contains(model, "glm-5-3-flash"):
		return 0.15, 0.50, true
	case strings.Contains(model, "glm-5.3"), strings.Contains(model, "glm-5-3"):
		return 1.40, 4.40, true
	case strings.Contains(model, "glm-5.2"), strings.Contains(model, "glm-5-2"):
		return 1.40, 4.40, true
	case strings.Contains(model, "glm-5.1"), strings.Contains(model, "glm-5-1"):
		// GLM-5.1: mesmo tier da 5.2 no pricing oficial (docs.z.ai).
		return 1.40, 4.40, true
	case strings.Contains(model, "glm-5-turbo"), strings.Contains(model, "glm-5v-turbo"):
		return 1.20, 4.00, true
	case strings.Contains(model, "glm-5"):
		return 1.00, 3.20, true
	// GLM-4.x list prices (docs.z.ai/guides/overview/pricing, Set/2026).
	// Variantes "-flash" são gratuitas (known=true com tarifa zero, como
	// Ollama); "-flashx" e "-air/-airx/-x" têm tier próprio e precisam
	// vir antes do id base da geração, que é prefixo delas.
	case strings.Contains(model, "glm-4.7-flashx"):
		return 0.07, 0.40, true
	case strings.Contains(model, "glm-4.7-flash"), strings.Contains(model, "glm-4.5-flash"),
		strings.Contains(model, "glm-4.6v-flash") && !strings.Contains(model, "flashx"):
		return 0, 0, true
	case strings.Contains(model, "glm-4.6v-flashx"):
		return 0.04, 0.40, true
	case strings.Contains(model, "glm-4.6v"):
		return 0.30, 0.90, true
	case strings.Contains(model, "glm-4.5-airx"):
		return 1.10, 4.50, true
	case strings.Contains(model, "glm-4.5-air"):
		return 0.20, 1.10, true
	case strings.Contains(model, "glm-4.5-x"):
		return 2.20, 8.90, true
	case strings.Contains(model, "glm-4.5v"):
		return 0.60, 1.80, true
	case strings.Contains(model, "glm-4.7"), strings.Contains(model, "glm-4.6"), strings.Contains(model, "glm-4.5"):
		return 0.60, 2.20, true
	}
	return 0, 0, false
}

// deepseekPricing — geração V4 (api-docs.deepseek.com, Aug 2026). A
// DeepSeek cobra peak/off-peak (off-peak = metade); cost_tracker usa o
// preço de pico para não sub-reportar. V4 específicos antes dos legados.
// "deepseek-reasoner" é o alias de API do R1 — mesma tarifa.
func deepseekPricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "deepseek-v4-pro"):
		return 1.32, 3.96, true
	case strings.Contains(model, "deepseek-v4"):
		// deepseek-v4-flash e futuros ids v4 sem tier próprio.
		return 0.44, 1.32, true
	case strings.Contains(model, "deepseek-r1"), strings.Contains(model, "deepseek-reasoner"):
		return 0.55, 2.19, true
	case strings.Contains(model, "deepseek"):
		return 0.27, 1.10, true
	}
	return 0, 0, false
}

// overriddenPricing resolves a CHATCLI_MODEL_PRICING entry for
// provider+model: the exact id first, then the catalog id an alias or
// variant resolves to (so "DEVIN:claude-opus-5=5/25" also prices
// claude-opus-5-xhigh), then the provider's wildcard.
func overriddenPricing(provider, model string) (float64, float64, bool) {
	if r, ok := LookupOverride(provider, model); ok {
		return r.InputPerMTok, r.OutputPerMTok, true
	}
	if meta, ok := catalog.Resolve(provider, model); ok && !strings.EqualFold(meta.ID, model) {
		if r, ok := LookupOverride(provider, meta.ID); ok {
			return r.InputPerMTok, r.OutputPerMTok, true
		}
	}
	return 0, 0, false
}

// devinListedPricing returns the per-account rate the Devin CLI reported
// for model (exact id first, then the catalog family a variant or alias
// resolves to, so "opus" or an unlisted reasoning suffix still price at
// the family rate). ok=false when the account listing never ran.
func devinListedPricing(model string) (float64, float64, bool) {
	if r, ok := Lookup(catalog.ProviderDevin, model); ok {
		return r.InputPerMTok, r.OutputPerMTok, true
	}
	if meta, ok := catalog.Resolve(catalog.ProviderDevin, model); ok && !strings.EqualFold(meta.ID, model) {
		if r, ok := Lookup(catalog.ProviderDevin, meta.ID); ok {
			return r.InputPerMTok, r.OutputPerMTok, true
		}
	}
	return 0, 0, false
}

// providerFallbackPricing handles families whose model IDs are ambiguous
// or where the provider name is the most reliable signal (proprietary
// wrappers like Copilot, local backends like Ollama). The known flag is
// true for every explicit case — including the deliberately-zero backends
// (Ollama/StackSpot/Devin, unmetered from ChatCLI's viewpoint) — and false
// only on the final fallthrough, where the model is genuinely unpriced.
//
// Moonshot (Kimi) — kimi-k3 public list price as of 2026-07 is $3.00/M
// input (cache miss; cache hit is $0.30/M) and $15.00/M output — priced
// well above the K2 line, so it gets its own case BEFORE the generic kimi
// match (specific beats generic, same ordering rule as claudePricing).
// kimi-k2.6 as of 2026-05 is $0.95/M input (cache miss) and $4.00/M
// output; cache-hit input is $0.16/M but cost_tracker only models a
// single tier — we charge the miss price so accounting stays
// conservative. Retired ids (kimi-k2.5, moonshot-v1-*) keep this
// tier so old session logs still price instead of reporting zero.
func providerFallbackPricing(provider, model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "minimax-m3"):
		// MiniMax-M3 (platform.minimax.io pricing-paygo, Set/2026): $0.30/
		// $1.20 até 512K de contexto (2x acima — não modelado), cache read
		// $0.06. Específico antes do genérico da família.
		return 0.30, 1.20, true
	case strings.Contains(model, "minimax"), strings.Contains(provider, "minimax"):
		return 0.20, 1.10, true
	case strings.Contains(provider, "zai"), strings.Contains(model, "glm"):
		return 0.50, 0.50, true
	case strings.HasPrefix(model, "kimi-k3"):
		return 3.00, 15.00, true
	case strings.HasPrefix(model, "kimi-k2.7-code-highspeed"):
		// K2.7 Code highspeed (platform.kimi.ai pricing, Aug 2026): 2× o
		// tier padrão K2.x — specific beats generic, senão o match "kimi"
		// abaixo cobraria a metade.
		return 1.90, 8.00, true
	case strings.Contains(provider, "moonshot"), strings.HasPrefix(model, "kimi"), strings.HasPrefix(model, "moonshot"):
		// Cobre também kimi-k2.7-code ($0.95/$4.00 — mesmo tier do K2.6).
		return 0.95, 4.00, true
	case strings.Contains(provider, "copilot"):
		// GitHub Copilot is a subscription: requests debit the plan's
		// premium-request allowance, not a per-token invoice. Known-zero
		// like Devin/Ollama/StackSpot; /cost labels it as such.
		return 0, 0, true
	case strings.Contains(provider, "openrouter"):
		return getOpenRouterModelPricing(model)
	case strings.Contains(provider, "ollama"), strings.Contains(provider, "stackspot"),
		strings.Contains(provider, "devin"):
		// Devin CLI sem listagem da conta: tarifa desconhecida mas
		// "conhecida-zero", como Ollama/StackSpot (o ramo DEVIN acima já
		// devolveu a tarifa listada quando ela existe).
		return 0, 0, true
	}
	return 0, 0, false
}

// novaPricing prices the Amazon Nova family on Bedrock (on-demand, us-east-1
// list prices per 1M tokens: Micro $0.035/$0.14, Lite $0.06/$0.24, Pro
// $0.80/$3.20, Premier $2.50/$12.50). Nova 2 generations are reported as
// unpriced until their list price is verified — never a guess.
func novaPricing(model string) (float64, float64, bool) {
	if !strings.Contains(model, "nova") {
		return 0, 0, false
	}
	switch {
	case strings.Contains(model, "nova-2"):
		return 0, 0, false
	case strings.Contains(model, "nova-micro"):
		return 0.035, 0.14, true
	case strings.Contains(model, "nova-lite"):
		return 0.06, 0.24, true
	case strings.Contains(model, "nova-pro"):
		return 0.80, 3.20, true
	case strings.Contains(model, "nova-premier"):
		return 2.50, 12.50, true
	}
	return 0, 0, false
}

// CacheRates returns the cache write and cache read price of
// provider+model in USD per million tokens. Rates follow each provider's
// published discount over the model's input price; families without a
// distinct published cache rate return zero (their cache reads are then
// billed at the plain input price, because RecordCost's subset carve-out
// is a no-op when the read rate is zero).
func CacheRates(provider, model string) (cacheWritePerMTok, cacheReadPerMTok float64) {
	return getCachePricing(provider, model)
}

// getCachePricing is CacheRates.
func getCachePricing(provider, model string) (cacheWriteCost, cacheReadCost float64) {
	model = strings.ToLower(model)
	inputCost, _, known := lookupModelPricing(provider, model)
	if !known || inputCost <= 0 {
		return 0, 0
	}

	switch {
	case strings.Contains(model, "fable-5-1"), strings.Contains(model, "fable-5.1"):
		// Fable 5.1: cache reads at $0.25/MTok = 2.5% of the $10 input
		// price (platform.claude.com/docs/en/models/fable-5-1/overview);
		// writes keep the 1.25x rule ($12.50). Must precede the generic
		// Claude case, which would bill reads at 10% ($1).
		return inputCost * 1.25, inputCost * 0.025
	case strings.Contains(model, "opus-5-5"), strings.Contains(model, "opus-5.5"):
		// Opus 5.5: cache reads at $0.20/MTok = 5% of the $4 input price
		// (platform.claude.com/docs/en/about-claude/pricing); writes keep
		// the 1.25x rule ($5). Must precede the generic Claude case, which
		// would bill reads at 10% ($0.40).
		return inputCost * 1.25, inputCost * 0.05
	case strings.Contains(model, "claude"):
		// Anthropic: write = 1.25x input, read = 0.1x input.
		return inputCost * 1.25, inputCost * 0.10
	case strings.Contains(model, "grok"):
		// xAI (docs.x.ai/developers/pricing, Set/2026): cached input
		// $0.50 em grok-4.7/4.6 (25% do input), $0.30 em grok-4.5 (15%),
		// $0.20 nos demais (16% de $1.25). Sem surcharge de escrita.
		switch {
		case strings.Contains(model, "grok-4.7"), strings.Contains(model, "grok-4.6"):
			return 0, inputCost * 0.25
		case strings.Contains(model, "grok-4.5"):
			return 0, inputCost * 0.15
		}
		return 0, inputCost * 0.16
	case strings.Contains(model, "gemini"):
		// Google context caching (implicit or explicit): cached reads at
		// 10% of input across the 2.5/3.x families
		// (ai.google.dev/gemini-api/docs/pricing, Sep/2026). Explicit
		// resources add storage per token-hour, priced separately from
		// lifecycle events (cli/cost_cache_resources.go).
		return 0, inputCost * 0.10
	case strings.Contains(model, "gpt-6"), strings.Contains(model, "gpt-5.6"):
		// gpt-6-astra e a família 5.6 deixaram o caching automático das
		// gerações anteriores: a tabela de preços publica coluna própria
		// de cache writes a 1,25x o input e cached input a 10% dele —
		// astra $12,50/$1,00 sobre $10; sol $5,00/$0,40 sobre $4; terra
		// $2,50/$0,20; luna $0,25/$0,02 (developers.openai.com/api/docs
		// /pricing, confirmado nas mesmas razões no mirror do OpenRouter).
		// Sem este caso os dois caíam no genérico abaixo, que cobrava a
		// leitura de cache a 50% do input — 5x acima da tarifa real.
		// A escrita fica inerte enquanto a usage da OpenAI não reporta
		// tokens de cache creation (só Anthropic/Bedrock/Devin o fazem);
		// está aqui para o dia em que reportar.
		return inputCost * 1.25, inputCost * 0.10
	case strings.Contains(model, "gpt"), strings.Contains(model, "o1"),
		strings.Contains(model, "o3"), strings.Contains(model, "o4"):
		// OpenAI automatic prompt caching: hits at 50% of input, no write
		// surcharge (platform.openai.com/docs/pricing).
		return 0, inputCost * 0.50
	case strings.Contains(model, "deepseek"):
		// DeepSeek cache hit ≈ 25% of the miss price.
		return 0, inputCost * 0.25
	case strings.Contains(model, "kimi"), strings.Contains(model, "moonshot"):
		// Moonshot cache hit ($0.16/M vs $0.95/M miss) ≈ 17% of input.
		return 0, inputCost * 0.17
	}
	return 0, 0
}

// getOpenRouterModelPricing returns pricing for models accessed via OpenRouter.
// Table-derived estimates only — when the OpenRouter response carries
// usage.cost, that actual billed amount overrides these numbers entirely.
func getOpenRouterModelPricing(model string) (inputCost, outputCost float64, known bool) {
	// OpenRouter passes through pricing from upstream providers. The known
	// flag PROPAGATES from the family lookup: a slug that matches a family
	// substring but no actual pricing entry stays "unpriced" so /cost lists
	// it instead of silently reporting it as free.
	switch {
	case strings.Contains(model, "claude"):
		return lookupModelPricing("anthropic", model)
	case strings.Contains(model, "gpt"):
		return lookupModelPricing("openai", model)
	case strings.Contains(model, "gemini"):
		return lookupModelPricing("google", model)
	case strings.Contains(model, "deepseek"):
		return lookupModelPricing("deepseek", model)
	case strings.Contains(model, "llama"):
		return 0.20, 0.20, true
	case strings.Contains(model, "mistral"):
		return 0.20, 0.60, true
	case strings.Contains(model, "qwen"):
		return 0.15, 0.15, true
	}
	return 0, 0, false
}

// CacheWrite1hPerMTok is the per-million price of a cache write with the
// 1-hour TTL, given the model's 5-minute write price. Anthropic bills it at
// 2x the input price (versus 1.25x for 5 minutes); every other family has
// no extended TTL and keeps its regular write rate.
func CacheWrite1hPerMTok(provider, model string, write5mPerMTok float64) float64 {
	return cacheWrite1hCost(provider, model, write5mPerMTok)
}

// cacheWrite1hCost is CacheWrite1hPerMTok.
func cacheWrite1hCost(provider, model string, write5m float64) float64 {
	m := strings.ToLower(model)
	if strings.Contains(m, "claude") || strings.Contains(m, "fable") || strings.Contains(strings.ToLower(provider), "claudeai") {
		if inputCost, _, known := lookupModelPricing(provider, model); known && inputCost > 0 {
			return inputCost * 2.0
		}
	}
	return write5m
}
