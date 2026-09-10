/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"sort"
	"strings"
)

// devinRate is the per-account price Cognition charges for a model family
// served through the Devin CLI, in USD per million tokens. fastIn/fastOut
// is the rate of the family's "-fast" / "-priority" variants (the CLI
// lists them at 2x, sometimes more); zero means the family has no such
// variant and the base rate applies to every suffix.
type devinRate struct {
	in, out         float64
	fastIn, fastOut float64
}

// devinStaticRates mirrors the cost_summary column of `devin models list`
// as an account listed it on 2 Sep 2026 (the fixture in
// llm/devincli/testdata/models_list.json is that listing). It is the
// fallback for a deployment whose CLI lists models WITHOUT cost_summary —
// enterprise builds do — so /cost still estimates instead of reporting
// zero. Precedence in lookupModelPricing: CHATCLI_MODEL_PRICING, then the
// rate the account itself lists, then this table. These are Cognition's
// rates, not the vendors' list prices: gpt-5.6-sol is $1.2/$6 here
// against OpenAI's own card, so borrowing the direct-API tables would be
// wrong, which is why families the listing never carried (fable, grok,
// adaptive) stay unpriced rather than guessed.
//
// Keys are family slugs with dots; lookups normalize "-" and "." so the
// CLI's hyphenated variant uids (claude-opus-4-8-medium-fast) hit the same
// row as the dotted slug (claude-opus-4.8). Longest key wins, so
// gpt-5.4-mini never resolves as gpt-5.4.
var devinStaticRates = map[string]devinRate{
	// Anthropic family.
	"claude-opus-5":     {5, 25, 10, 50},
	"claude-sonnet-5":   {2, 10, 0, 0},
	"claude-opus-4.8":   {5, 25, 10, 50},
	"claude-opus-4.7":   {5, 25, 0, 0},
	"claude-opus-4.6":   {5, 25, 0, 0},
	"claude-opus-4.5":   {5, 25, 0, 0},
	"claude-sonnet-4.6": {3, 15, 0, 0},
	"claude-sonnet-4.5": {3, 15, 0, 0},
	"claude-haiku-4.5":  {1, 5, 0, 0},
	// OpenAI family ("-priority" is the listing's spelling of fast).
	"gpt-5.6-sol":   {1.2, 6, 8, 40},
	"gpt-5.6-terra": {2, 12, 4, 24},
	"gpt-5.6-luna":  {0.2, 1.2, 0.4, 2.4},
	"gpt-5.5":       {5, 30, 12.5, 75},
	"gpt-5.4-mini":  {0.75, 4.5, 0, 0},
	"gpt-5.4":       {2.5, 15, 5, 30},
	"gpt-5.3-codex": {1.75, 14, 3.5, 28},
	"gpt-5.2":       {1.75, 14, 0, 0},
	"gpt-5.1":       {1.25, 10, 0, 0},
	"gpt-4.1":       {2, 8, 0, 0},
	// Google family.
	"gemini-3.7-flash": {1.5, 7.5, 0, 0},
	"gemini-3.6-flash": {1.5, 7.5, 0, 0},
	"gemini-3.5-flash": {1.5, 9, 0, 0},
	"gemini-3.1-pro":   {2, 12, 0, 0},
	"gemini-3-flash":   {0.5, 3, 0, 0},
	// Z.AI, Moonshot, DeepSeek.
	"glm-5.3-flash":     {0.15, 0.5, 0, 0},
	"glm-5.3":           {1.4, 4.4, 0, 0},
	"glm-5.2":           {1.4, 4.4, 0, 0},
	"kimi-k3":           {3, 15, 0, 0},
	"kimi-k2.7":         {0.95, 4, 0, 0},
	"kimi-k2.6":         {0.8, 2, 0, 0},
	"deepseek-v4-pro":   {1.32, 3.96, 0, 0},
	"deepseek-v4-flash": {0.14, 0.28, 0, 0},
	// Cognition in-house. swe-1.6-fast is a family of its own at the base
	// rate, not a fast variant of swe-1.6 — hence its own row.
	"swe-1.7-lightning": {2.5, 12.5, 0, 0},
	"swe-1.7":           {0.5, 2.5, 0, 0},
	"swe-1.6-fast":      {0.5, 2.5, 0, 0},
	"swe-1.6":           {0.5, 2.5, 0, 0},
}

// devinStaticKeys is devinStaticRates' keys, normalized and longest first,
// so prefix matching picks the most specific family.
var devinStaticKeys = func() []string {
	keys := make([]string, 0, len(devinStaticRates))
	for k := range devinStaticRates {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}()

// normalizeDevinID folds the two spellings the CLI uses for one model —
// dotted slug and hyphenated uid — onto one form.
func normalizeDevinID(id string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(id)), ".", "-")
}

// devinStaticPricing returns the static Cognition rate for a Devin model
// id, family slug or variant uid, applying the fast/priority rate when the
// suffix asks for it. ok=false for a family the listing never priced.
func devinStaticPricing(model string) (inputCost, outputCost float64, ok bool) {
	norm := normalizeDevinID(model)
	for _, key := range devinStaticKeys {
		nk := normalizeDevinID(key)
		if !strings.HasPrefix(norm, nk) {
			continue
		}
		rest := norm[len(nk):]
		// A longer id must continue with a separator: "gpt-5.5" is not a
		// prefix match for "gpt-5.5x", only for "gpt-5.5-medium".
		if rest != "" && rest[0] != '-' {
			continue
		}
		r := devinStaticRates[key]
		if r.fastIn > 0 && (strings.Contains(rest, "-fast") || strings.Contains(rest, "-priority")) {
			return r.fastIn, r.fastOut, true
		}
		return r.in, r.out, true
	}
	return 0, 0, false
}
