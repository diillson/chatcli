/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package catalog

import "strings"

// DEVIN provider entries — the models served through the local Devin CLI
// wrapper (llm/devincli). The list mirrors what `devin --model` accepts on
// an enterprise install (Jul 2026). Two things to know before editing:
//
//  1. Specs here are CLIENT-SIDE BOOKKEEPING ONLY (compaction budget, ctx%
//     footer, /metrics): the Devin backend enforces its own limits
//     server-side. Values mirror each family's provider specs where
//     published; the swe-* family (Cognition in-house) has no published
//     specs, so it gets a conservative profile. CHATCLI_CONTEXT_WINDOW
//     overrides everything when a deployment differs.
//
//  2. Ordering: newest-first within each family. Resolve()'s tier-2 alias
//     pass matches by prefix, and Devin's slugs use dots ("claude-sonnet-4"
//     is a prefix of "claude-sonnet-4.5"), so an older entry listed first
//     would shadow non-exact lookups of newer ones.
//
// The entries are appended to the registry at init; the per-provider filter
// in Resolve() keeps them from ever shadowing other providers' models.
func init() {
	type devinModel struct {
		id  string
		ctx int
		out int
	}
	devinModels := []devinModel{
		// Anthropic family (specs mirror the CLAUDEAI entries). Fable 5.1
		// (Cognition blog, Aug 31 2026) and Fable 5 are back in the Devin
		// model table; 5.1 MUST precede 5 (dotted prefix rule above).
		// Fable 5.2 (docs.devin.ai/desktop/models, Nov 9 2026 table:
		// claude-fable-5-2-low .. -max, plus -fast) precedes 5.1 and 5.
		{"claude-fable-5.2", 1000000, 128000},
		{"claude-fable-5.1", 1000000, 128000},
		{"claude-fable-5", 1000000, 128000},
		// Opus 5.5 (Sep 22 2026) precedes Opus 5: dotted prefix rule.
		{"claude-opus-5.5", 1000000, 128000},
		// Sonnet 5.5 and Haiku 5.5 (Nov 9 2026 table) precede Sonnet 5.
		{"claude-sonnet-5.5", 1000000, 128000},
		{"claude-haiku-5.5", 1000000, 128000},
		{"claude-opus-5", 1000000, 128000},
		{"claude-sonnet-5", 1000000, 128000},
		{"claude-opus-4.8", 1000000, 128000},
		{"claude-opus-4.7", 1000000, 128000},
		{"claude-opus-4.6", 1000000, 128000},
		{"claude-sonnet-4.6", 1000000, 64000},
		{"claude-opus-4.5", 200000, 64000},
		{"claude-sonnet-4.5", 200000, 64000},
		{"claude-haiku-4.5", 200000, 64000},
		{"claude-sonnet-4", 200000, 64000},
		// OpenAI family (specs mirror the OPENAI entries). Devin exposes
		// Astra as five effort-suffixed slugs (gpt-6-astra-low ..
		// -max, with -medium flagged recommended); the bare id below
		// covers all of them through Resolve()'s prefix pass, the same
		// way the gpt-5.6-* entries cover their own -medium/-high forms.
		// GPT-6.1 Sol (Cognition blog, Sep 29 2026: Devin Desktop + CLI at
		// the $2/$10 list price). The slug is not published; this follows
		// the vendor id the gpt-6-* rows already use.
		// GPT-6.1 Terra (Nov 9 2026 table: gpt-6-1-terra-*).
		{"gpt-6.1-terra", 1050000, 128000},
		{"gpt-6.1-sol", 1050000, 128000},
		{"gpt-6-astra", 1050000, 128000},
		{"gpt-6-sol", 1050000, 128000},
		{"gpt-6-luna", 1050000, 128000},
		{"gpt-5.6-sol", 1050000, 128000},
		{"gpt-5.6-terra", 1050000, 128000},
		{"gpt-5.6-luna", 1050000, 128000},
		{"gpt-5.5", 1050000, 128000},
		{"gpt-5.4-mini", 200000, 100000},
		{"gpt-5.4", 200000, 100000},
		{"gpt-5.3-codex", 200000, 100000},
		{"gpt-5.2", 200000, 100000},
		{"gpt-5.1", 400000, 128000},
		{"gpt-4.1", 1047576, 32768},
		// Google family (Gemini 3.x: 1M window / 65K output).
		// Gemini 4 Argon and 3.8 Flash (Nov 9 2026 table).
		{"gemini-4-argon", 1048576, 65536},
		{"gemini-3.8-flash", 1048576, 65536},
		{"gemini-3.7-flash", 1048576, 65536},
		{"gemini-3.6-flash", 1048576, 65536},
		{"gemini-3.5-flash", 1048576, 65536},
		{"gemini-3.1-pro", 1048576, 65536},
		{"gemini-3-flash", 1048576, 65536},
		// Others with published specs. Kimi K3: 1M window (Kimi Delta
		// Attention); output capped at the K2.x family's 128K — OpenRouter
		// lists output = context, which is an unbounded-listing artifact,
		// not a real per-request cap.
		{"glm-5.3", 1000000, 128000},
		{"glm-5.2", 1000000, 128000},
		// Kimi K3.1 Pro (Nov 9 2026 table) precedes K3, its prefix.
		{"kimi-k3.1-pro", 1048576, 131072},
		{"kimi-k3", 1048576, 131072},
		{"kimi-k2.7", 262144, 131072},
		{"kimi-k2.6", 262144, 131072},
		{"deepseek-v4-pro", 1000000, 32000},
		{"deepseek-v4-flash", 1000000, 32000},
		// xAI (docs.devin.ai/desktop/models: grok-4-6-* / grok-4-5-*):
		// 500K window per docs.x.ai; output follows the XAI block's 16K
		// convention since xAI publishes no cap.
		{"grok-4.7", 500000, 16384},
		{"grok-4.6", 500000, 16384},
		{"grok-4.5", 500000, 16384},
		// Cognition in-house (Windsurf SWE line): no published specs —
		// conservative profile until Cognition documents them.
		// SWE-2 (Sep 10 2026) and SWE-2 Flash (Nov 4 2026): flash first,
		// swe-2 is its prefix.
		{"swe-2-flash", 200000, 32000},
		{"swe-2", 200000, 32000},
		{"swe-1.7-lightning", 200000, 32000},
		{"swe-1.7", 200000, 32000},
		{"swe-1.6-fast", 200000, 32000},
		{"swe-1.6", 200000, 32000},
	}

	entries := make([]ModelMeta, 0, len(devinModels))
	for _, m := range devinModels {
		// The CLI names a model twice: the dotted family slug and the
		// hyphenated variant uid (claude-fable-5-2-high, gpt-6-1-terra-
		// medium). Both spellings are aliases, so a uid resolves to its own
		// row instead of to the shorter sibling it used to fall through to
		// (claude-fable-5-2-high → claude-fable-5) or to nothing at all
		// (grok-4-7-high).
		aliases := []string{m.id}
		if dashed := strings.ReplaceAll(m.id, ".", "-"); dashed != m.id {
			aliases = append(aliases, dashed)
		}
		entries = append(entries, ModelMeta{
			ID:          m.id,
			Aliases:     aliases,
			DisplayName: m.id + " (Devin)",
			Provider:    ProviderDevin,
			// No "vision" on purpose: the devincli transport is flat text,
			// so image attachments never reach the backing model.
			Capabilities:    []string{"tools"},
			ContextWindow:   m.ctx,
			MaxOutputTokens: m.out,
			PreferredAPI:    APIChatCompletions,
		})
	}

	mu.Lock()
	registry = append(registry, entries...)
	mu.Unlock()
}
