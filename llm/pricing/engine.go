/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pricing

import (
	"strings"

	"github.com/diillson/chatcli/models"
)

// ModelRates is every knob the cost formula needs for one provider+model:
// the list prices, the cache discounts and the two schema facts that
// decide how the provider's counts are turned into billable tokens.
type ModelRates struct {
	// InputPerMTok and OutputPerMTok are the list prices in USD per
	// million tokens.
	InputPerMTok  float64
	OutputPerMTok float64
	// CacheWritePerMTok and CacheReadPerMTok are the prompt-cache prices;
	// zero where the family publishes no distinct cache rate (reads are
	// then billed at the plain input price).
	CacheWritePerMTok float64
	CacheReadPerMTok  float64
	// Known is false when the model matched no pricing table entry: a
	// zero rate then means "price unknown", not "free". Subscription and
	// self-hosted backends (Copilot, Ollama, StackSpot, an unlisted Devin
	// family, the GLM Coding Plan) are Known with zero rates.
	Known bool
	// CacheTokensAdditive reports whether the provider counts cache tokens
	// ALONGSIDE the prompt count (Anthropic Messages, Bedrock Converse)
	// rather than as a subset of it (OpenAI, Gemini, OpenRouter).
	CacheTokensAdditive bool
	// ReasoningTokensAdditive reports whether reasoning tokens are counted
	// outside the completion count (Gemini) and billed on top of it.
	ReasoningTokensAdditive bool
}

// RatesFor resolves the rates of provider+model from every source the
// engine knows, in precedence order: the CHATCLI_MODEL_PRICING override,
// the per-account rate a provider listed at runtime, then the static
// tables. It is the single lookup every surface (CLI, gRPC server,
// operator) prices usage with.
func RatesFor(provider, model string) ModelRates {
	in, out, known := lookupModelPricing(provider, model)
	write, read := getCachePricing(provider, model)
	return ModelRates{
		InputPerMTok:            in,
		OutputPerMTok:           out,
		CacheWritePerMTok:       write,
		CacheReadPerMTok:        read,
		Known:                   known,
		CacheTokensAdditive:     CacheTokensAdditive(provider, model),
		ReasoningTokensAdditive: ReasoningTokensAdditive(provider),
	}
}

// Cost is a priced amount of usage, broken down the way /cost reports it.
type Cost struct {
	// InputUSD prices the billable (uncached, unbilled) prompt tokens.
	InputUSD float64
	// OutputUSD prices the completion tokens (plus additive reasoning).
	OutputUSD float64
	// CacheUSD prices cache writes (5-minute and 1-hour shares) and reads.
	CacheUSD float64
	// TotalUSD is the sum of the three parts plus any provider-billed
	// amount the input carried.
	TotalUSD float64
	// Known mirrors ModelRates.Known: false means the zero is ignorance,
	// not a free model.
	Known bool
}

// RecordInput is the cumulative token ledger of one provider+model that
// RecordCost prices: the raw counts as the provider reported them plus the
// Billed* pools, which remember the tokens a provider-billed amount
// (ProviderCostUSD) already covers so the tables price only the rest.
type RecordInput struct {
	Provider string
	Model    string

	// PromptTokens is the prompt count under the record convention:
	// "uncached delta" on additive schemas, "whole input" on subset ones
	// (see PromptTokensForRecord).
	PromptTokens     int64
	CompletionTokens int64
	// ReasoningTokens is informational except where
	// ReasoningTokensAdditive says they are billed on top of completion.
	ReasoningTokens int64

	CacheCreationTokens int64
	CacheReadTokens     int64
	// CacheCreation1hTokens is the share of CacheCreationTokens written
	// with the 1-hour TTL (billed at 2x input instead of 1.25x).
	CacheCreation1hTokens int64

	// ProviderCostUSD is the amount the provider itself billed for the
	// calls whose tokens live in the Billed* pools; it is added to the
	// total verbatim.
	ProviderCostUSD           float64
	BilledPromptTokens        int64
	BilledCompletionTokens    int64
	BilledCacheReadTokens     int64
	BilledCacheCreationTokens int64
}

// RecordCost prices one record — the ONLY cost formula in ChatCLI. Table
// math prices only the tokens NOT covered by a provider-billed amount:
// billed calls' tokens live in the Billed* pools and their cost is
// ProviderCostUSD verbatim, so a mixed key (some calls report cost, some
// do not) adds both parts instead of letting one clobber the other.
func RecordCost(in RecordInput) Cost {
	inputCost, outputCost, known := lookupModelPricing(in.Provider, in.Model)
	cacheWriteCost, cacheReadCost := getCachePricing(in.Provider, in.Model)

	unbilled := func(total, billed int64) int64 {
		if d := total - billed; d > 0 {
			return d
		}
		return 0
	}
	promptTokens := unbilled(in.PromptTokens, in.BilledPromptTokens)
	completionTokens := unbilled(in.CompletionTokens, in.BilledCompletionTokens)
	cacheRead := unbilled(in.CacheReadTokens, in.BilledCacheReadTokens)
	cacheCreation := unbilled(in.CacheCreationTokens, in.BilledCacheCreationTokens)

	// Gemini reports thinking tokens (thoughtsTokenCount) OUTSIDE the
	// candidates count and bills them as output; OpenAI's reasoning tokens
	// are already inside completion_tokens. Add them only where additive.
	if ReasoningTokensAdditive(in.Provider) {
		completionTokens += unbilled(in.ReasoningTokens, 0)
	}

	billableInput := promptTokens
	if !CacheTokensAdditive(in.Provider, in.Model) && cacheRead > 0 && cacheReadCost > 0 {
		// OpenAI/Gemini-style usage reports cached tokens as a SUBSET of the
		// prompt count — carve them out so they are billed once, at the
		// discounted cache-read rate, instead of twice. Only when a discount
		// rate exists: for families without a published cache rate the
		// carve-out would make cached tokens FREE, so they stay billed at
		// the plain input price instead (conservative).
		billableInput -= cacheRead
		if billableInput < 0 {
			billableInput = 0
		}
	}

	var c Cost
	c.Known = known
	c.InputUSD = float64(billableInput) / 1_000_000 * inputCost
	c.OutputUSD = float64(completionTokens) / 1_000_000 * outputCost
	// The 1-hour TTL share of the write is billed at a higher rate (2x
	// input on Anthropic versus 1.25x for the 5-minute default).
	creation1h := in.CacheCreation1hTokens
	if creation1h > cacheCreation {
		creation1h = cacheCreation
	}
	creation5m := cacheCreation - creation1h
	c.CacheUSD = float64(creation5m)/1_000_000*cacheWriteCost +
		float64(creation1h)/1_000_000*cacheWrite1hCost(in.Provider, in.Model, cacheWriteCost) +
		float64(cacheRead)/1_000_000*cacheReadCost
	c.TotalUSD = c.InputUSD + c.OutputUSD + c.CacheUSD + in.ProviderCostUSD
	return c
}

// CostOf prices one provider-reported usage payload with the same rules
// the session tracker applies — cache semantics, long-context tiers,
// provider-billed amounts — so a footer, a server response and a
// Kubernetes cost ledger never disagree about the same call. A
// provider-reported cost wins outright; a call past the provider's
// long-context threshold is priced at its tier; everything else runs
// through RecordCost. A nil usage prices as zero and unknown.
func CostOf(provider, model string, u *models.UsageInfo) Cost {
	if u == nil {
		return Cost{}
	}
	if u.CostUSD > 0 {
		return Cost{TotalUSD: u.CostUSD, Known: true}
	}
	if tiered := tieredCallCost(provider, model, u); tiered.TotalUSD > 0 {
		return tiered
	}
	return RecordCost(callRecordInput(provider, model, u))
}

// callRecordInput is the RecordInput of a single call: what the tracker
// books for that call before any other call of the same key is added.
func callRecordInput(provider, model string, u *models.UsageInfo) RecordInput {
	return RecordInput{
		Provider:              provider,
		Model:                 model,
		PromptTokens:          int64(PromptTokensForRecord(provider, model, u)),
		CompletionTokens:      int64(u.CompletionTokens),
		ReasoningTokens:       int64(u.ReasoningTokens),
		CacheReadTokens:       int64(u.CacheReadInputTokens),
		CacheCreationTokens:   int64(u.CacheCreationInputTokens),
		CacheCreation1hTokens: int64(u.CacheCreation1hInputTokens),
	}
}

// ReasoningTokensAdditive reports whether a provider's reasoning tokens
// are reported outside the completion count (Gemini) and must be billed
// on top of it.
func ReasoningTokensAdditive(provider string) bool {
	switch strings.ToUpper(strings.TrimSpace(provider)) {
	case "GOOGLEAI", "GEMINI", "GOOGLE":
		return true
	}
	return false
}

// longContextThresholdTokens is the prompt size above which the providers
// below switch to their long-context tier.
const (
	longContextAnthropicTokens = 200_000
	longContextGeminiTokens    = 200_000
	longContextGrokTokens      = 128_000
)

// LongContextMultipliers returns the input and output price multipliers a
// call with promptTokens of context pays: Anthropic (Claude 4+ with the
// 1M window) and Gemini 2.5 Pro bill 2× input and 1.5× output past 200K;
// xAI Grok 4 bills 2× both past 128K. 1/1 elsewhere.
func LongContextMultipliers(provider, model string, promptTokens int) (in, out float64) {
	p, m := strings.ToLower(provider), strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude") && promptTokens > longContextAnthropicTokens:
		return 2, 1.5
	case strings.Contains(m, "gemini-2.5-pro") && promptTokens > longContextGeminiTokens:
		return 2, 1.5
	case (strings.Contains(p, "xai") || strings.HasPrefix(m, "grok-4")) && strings.HasPrefix(m, "grok-4") && promptTokens > longContextGrokTokens:
		return 2, 2
	}
	return 1, 1
}

// TieredCallCostUSD prices one call with its long-context tier applied; 0
// when the call is not in a tier (the record math prices it normally),
// when the provider already billed it, or when the model is unpriced.
func TieredCallCostUSD(provider, model string, u *models.UsageInfo) float64 {
	return tieredCallCost(provider, model, u).TotalUSD
}

// tieredCallCost is TieredCallCostUSD with the breakdown: the input and
// cache parts scaled by the input multiplier, the output part by the
// output multiplier. The zero Cost means "not tiered".
func tieredCallCost(provider, model string, u *models.UsageInfo) Cost {
	if u == nil || u.CostUSD > 0 {
		return Cost{}
	}
	// Schema-normalized: adding the cached counts unconditionally
	// double-counted them on subset schemas (Gemini, Grok), which could tip
	// a request over the long-context threshold it never crossed.
	in, out := LongContextMultipliers(provider, model, ContextTokens(provider, model, u))
	if in == 1 && out == 1 {
		return Cost{}
	}
	c := RecordCost(callRecordInput(provider, model, u))
	if !c.Known {
		return Cost{}
	}
	return Cost{
		InputUSD:  c.InputUSD * in,
		OutputUSD: c.OutputUSD * out,
		CacheUSD:  c.CacheUSD * in,
		TotalUSD:  (c.InputUSD+c.CacheUSD)*in + c.OutputUSD*out,
		Known:     true,
	}
}

// CacheTokensAdditive reports whether the usage payload counts cache
// tokens ALONGSIDE the prompt count (Anthropic Messages schema:
// input_tokens excludes cache reads/writes) rather than as a subset of it
// (OpenAI cached_tokens, Gemini cachedContentTokenCount). The semantics
// belong to the REPORTING SCHEMA, not the model name: a Claude model
// served through an OpenAI-compatible gateway (OpenRouter) reports
// subset-style cached_tokens, so the provider decides when it implies the
// schema.
func CacheTokensAdditive(provider, model string) bool {
	p := strings.ToLower(provider)
	m := strings.ToLower(model)
	if strings.Contains(p, "openrouter") {
		return false // OpenAI-compatible schema regardless of the model
	}
	if strings.Contains(p, "bedrock") {
		// Converse TokenUsage and the InvokeModel/Mantle Anthropic envelope
		// both report inputTokens as the NON-cached input only ("total
		// input tokens = inputTokens + cacheReadInputTokens +
		// cacheWriteInputTokens", Bedrock prompt-caching guide) — for
		// every vendor served through Converse, not just Claude. The
		// OpenAI family on Bedrock (gpt-oss InvokeModel, GPT-5.x on the
		// Responses/Chat Completions surface) keeps OpenAI's subset
		// semantics (input_tokens includes cached_tokens).
		return !strings.Contains(m, "gpt") && !strings.Contains(m, "openai")
	}
	return strings.Contains(m, "claude")
}

// PromptTokensForRecord converts one call's prompt count into the
// convention RecordCost prices PromptTokens under: "uncached delta" where
// CacheTokensAdditive says additive, "whole input" where it says subset.
// The convention is decided by provider and model NAME; the payload's
// schema is a fact the adapter recorded in InputTokensTotal, and the two
// disagree exactly when a model is served through a schema that is not
// its vendor's. The case seen in the field: an enterprise Devin CLI
// reports a Claude turn in the OpenAI-shaped ATIF block (prompt_tokens
// 14274 INCLUDING cached_tokens 9098 and a 5173-token cache write), the
// name rule says additive, and without this the 14271 cached tokens were
// billed at the input rate on top of their cache rate.
//
// Only PromptTokens is converted; the cache pools stay as reported and are
// priced once, at their own rates, either way. No InputTokensTotal (an
// estimate, or a session recorded by an older build) means no schema fact
// and the count is used as-is.
func PromptTokensForRecord(provider, model string, usage *models.UsageInfo) int {
	if usage == nil {
		return 0
	}
	cached := usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	if usage.InputTokensTotal <= 0 || cached == 0 {
		return usage.PromptTokens
	}
	payloadAdditive := usage.InputTokensTotal >= usage.PromptTokens+cached
	nameAdditive := CacheTokensAdditive(provider, model)
	switch {
	case nameAdditive && !payloadAdditive:
		// Subset payload under an additive convention: the prompt count is
		// the whole input, keep only the part outside the cache.
		if delta := usage.PromptTokens - cached; delta > 0 {
			return delta
		}
		return 0
	case !nameAdditive && payloadAdditive:
		// Additive payload under a subset convention: the whole input is
		// prompt + reads (RecordCost carves the reads back out at the
		// cache-read rate); writes stay outside so the write rate prices
		// them exactly once.
		return usage.PromptTokens + usage.CacheReadInputTokens
	}
	return usage.PromptTokens
}

// ContextTokens is the input the model actually held for the turn — what
// a ctx% figure must measure against the window. Additive schemas
// (Anthropic, Bedrock Converse) report cache reads and writes ALONGSIDE
// PromptTokens, so a long cached conversation shows a tiny PromptTokens
// (only the uncached delta) while the model is holding the whole
// history; subset schemas (OpenAI, Gemini, OpenRouter) already include
// cached tokens in PromptTokens. Without this, a Bedrock session at 60%
// of a 1M window rendered "ctx 2%" right before auto-compaction fired —
// the compactor sizes the real history, the footer sized the delta.
func ContextTokens(provider, model string, usage *models.UsageInfo) int {
	if usage == nil {
		return 0
	}
	// The provider adapter classified its own payload — believe it over any
	// guess made from the provider/model strings. This is what makes a
	// Claude model behave correctly whether it was served by Anthropic
	// (additive), by an OpenAI-compatible gateway (subset) or by a CLI that
	// proxies several backends.
	if usage.InputTokensTotal > 0 {
		return usage.InputTokensTotal
	}
	// Not normalized: usage restored from a session recorded by an older
	// build, or an estimate. Fall back to the name-based heuristic.
	n := usage.PromptTokens
	if CacheTokensAdditive(provider, model) {
		n += usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	}
	return n
}
