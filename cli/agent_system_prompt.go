/*
 * ChatCLI - Structured system prompt assembly for agent/coder modes.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/models"
)

// buildAgentSystemMessage composes the agent/coder-mode system message from
// the semantic blocks used by Run(), ordered most-stable-first so the
// cache_control:ephemeral breakpoints land on a CONTIGUOUS prefix the provider
// can serve as warm-cache reads. Every per-turn-volatile block is appended
// AFTER the last cached breakpoint and carries NO hint, so it can never
// invalidate a stable prefix above it.
//
// Anthropic caches by prefix: a breakpoint only hits when every byte before it
// matches a prior request. A volatile block placed inside the cached region
// (e.g. the hint-driven workspace/memory block, or the wall-clock timestamp)
// poisons every cached block below it — paying cache-creation cost each turn
// while never earning a read. Hence the stable/volatile split below.
//
// Stable across a session → cached prefix (each stamped ephemeral):
//
//	core         — persona / CoderSystemPrompt + format rules
//	tools        — plugin tool catalog + session workspace hint
//	orchestrator — agent registry catalog (multi-agent prompt)
//
// Volatile per turn → NO cache hint, appended after the cached prefix:
//
//	workspace    — bootstrap files + MEMORY retrieval (hint-driven)
//	skills       — pinned + auto-activated + manual skills (query-driven)
//	channels     — MCP push ring (newest messages each turn)
//	dynamic      — wall-clock timestamp + cwd disambiguation
//
// Two representations are produced and kept in sync:
//
//   - Message.Content — flat string with "\n\n" separators. Consumed by every
//     provider that does not interpret structured system blocks (OpenAI,
//     Gemini, Ollama, StackSpot, etc.) and by the accounting code that
//     measures history payload via len(Content).
//   - Message.SystemParts — one ContentBlock per non-empty block, with cache
//     hints only on the stable prefix.
//
// Cache budget: Anthropic allows up to 4 cache_control breakpoints; the three
// stable blocks fit with one to spare, and any empty block simply collapses.
func buildAgentSystemMessage(core, tools, workspace, skills, orchestrator, channels, dynamic string) models.Message {
	stable := []string{
		strings.TrimSpace(core),
		strings.TrimSpace(tools),
		strings.TrimSpace(orchestrator),
	}
	volatile := []string{
		strings.TrimSpace(workspace),
		strings.TrimSpace(skills),
		strings.TrimSpace(channels),
		strings.TrimSpace(dynamic),
	}

	var parts []models.ContentBlock
	var sb strings.Builder
	appendBlock := func(text string, cached bool) {
		if text == "" {
			return
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(text)
		block := models.ContentBlock{Type: "text", Text: text}
		if cached {
			block.CacheControl = &models.CacheControl{Type: "ephemeral"}
		}
		parts = append(parts, block)
	}

	for _, text := range stable {
		appendBlock(text, true)
	}
	for _, text := range volatile {
		appendBlock(text, false)
	}

	return models.Message{
		Role:        "system",
		Content:     sb.String(),
		SystemParts: parts,
	}
}
