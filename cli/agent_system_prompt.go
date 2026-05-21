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

// buildAgentSystemMessage composes the agent-mode system message from the
// semantic blocks used by Run(): core behavior, tools context, workspace
// context, (skills + orchestrator), and the volatile MCP channels block —
// trailing in stability order so the most stable content sits at the front
// of the cached prefix.
//
// Two representations are produced and kept in sync:
//
//   - Message.Content — flat string with "\n\n" separators. Consumed by
//     every provider that does not interpret structured system blocks
//     (OpenAI, Gemini, Ollama, StackSpot, etc.) and by all the accounting
//     code that measures history payload via len(Content).
//
//   - Message.SystemParts — one ContentBlock per non-empty block. Stable
//     blocks (core/tools/workspace + the skills+orchestrator tail) are
//     stamped with CacheControl{Type:"ephemeral"} so Anthropic serves
//     identical prefixes as cache reads on subsequent turns. The volatile
//     MCP channels block has NO cache hint — it changes every turn and
//     caching it would just thrash the breakpoint.
//
// Cache budget: Anthropic allows up to 4 cache_control breakpoints. The
// five stable inputs collapse whenever one is empty; when all are present
// we intentionally merge skills+orchestrator into a single tail block so
// the full set fits in 4 boundaries. The channels block is appended
// AFTER the cached tail without consuming a breakpoint.
func buildAgentSystemMessage(core, tools, workspace, skills, orchestrator, channels string) models.Message {
	core = strings.TrimSpace(core)
	tools = strings.TrimSpace(tools)
	workspace = strings.TrimSpace(workspace)
	skills = strings.TrimSpace(skills)
	orchestrator = strings.TrimSpace(orchestrator)
	channels = strings.TrimSpace(channels)

	// Merge the two most volatile cached blocks into a single trailing
	// block. Keeping skills+orchestrator on one boundary leaves room
	// for core/tools/workspace to each have their own stable breakpoint.
	tail := skills
	if orchestrator != "" {
		if tail != "" {
			tail += "\n\n"
		}
		tail += orchestrator
	}

	cached := []string{core, tools, workspace, tail}

	var parts []models.ContentBlock
	var sb strings.Builder
	for _, text := range cached {
		if text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(text)
		parts = append(parts, models.ContentBlock{
			Type:         "text",
			Text:         text,
			CacheControl: &models.CacheControl{Type: "ephemeral"},
		})
	}

	// Append the volatile MCP channels block last and without a cache
	// hint. The user sees the latest push messages on every turn,
	// while everything cacheable stays cacheable.
	if channels != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(channels)
		parts = append(parts, models.ContentBlock{
			Type: "text",
			Text: channels,
		})
	}

	return models.Message{
		Role:        "system",
		Content:     sb.String(),
		SystemParts: parts,
	}
}
