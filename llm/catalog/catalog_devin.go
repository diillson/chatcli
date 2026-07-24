/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package catalog

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
		// Anthropic family (specs mirror the CLAUDEAI entries).
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
		// OpenAI family (specs mirror the OPENAI entries).
		{"gpt-5.6-sol", 1050000, 128000},
		{"gpt-5.6-terra", 1050000, 128000},
		{"gpt-5.6-luna", 1050000, 128000},
		{"gpt-5.5", 1050000, 128000},
		{"gpt-5.4-mini", 200000, 100000},
		{"gpt-5.4", 200000, 100000},
		{"gpt-5.3-codex", 200000, 100000},
		{"gpt-5.2", 200000, 100000},
		// Google family (Gemini 3.x: 1M window / 65K output).
		{"gemini-3.5-flash", 1048576, 65536},
		{"gemini-3.1-pro", 1048576, 65536},
		{"gemini-3-flash", 1048576, 65536},
		// Others with published specs.
		{"glm-5.2", 1000000, 128000},
		{"kimi-k2.7", 262144, 131072},
		{"kimi-k2.6", 262144, 131072},
		{"deepseek-v4-pro", 1000000, 32000},
		// Cognition in-house (Windsurf SWE line): no published specs —
		// conservative profile until Cognition documents them.
		{"swe-1.7-lightning", 200000, 32000},
		{"swe-1.7", 200000, 32000},
		{"swe-1.6-fast", 200000, 32000},
		{"swe-1.6", 200000, 32000},
		{"swe-1.5", 200000, 32000},
	}

	entries := make([]ModelMeta, 0, len(devinModels))
	for _, m := range devinModels {
		entries = append(entries, ModelMeta{
			ID:          m.id,
			Aliases:     []string{m.id},
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
