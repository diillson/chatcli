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
// four semantic blocks used by Run(): core behavior, tools context,
// workspace context, and (skills + orchestrator) — trailing in stability
// order so the most stable content sits at the front of the cached prefix.
//
// Two representations are produced and kept in sync:
//
//   - Message.Content — flat string with "\n\n" separators. Consumed by
//     every provider that does not interpret structured system blocks
//     (OpenAI, Gemini, Ollama, StackSpot, etc.) and by all the accounting
//     code that measures history payload via len(Content).
//
//   - Message.SystemParts — one ContentBlock per non-empty block, each
//     stamped with CacheControl{Type:"ephemeral"}. Anthropic reads these
//     directly and serves identical prefixes as cache reads on subsequent
//     turns. Empty blocks are dropped so we never waste a cache breakpoint
//     on whitespace.
//
// The number of breakpoints stays within Anthropic's limit of 4: the
// five input blocks collapse whenever one is empty, and when all five
// are present we intentionally merge (skills + orchestrator) into the
// last block so the full set fits.
func buildAgentSystemMessage(core, tools, workspace, skills, orchestrator string) models.Message {
	core = strings.TrimSpace(core)
	tools = strings.TrimSpace(tools)
	workspace = strings.TrimSpace(workspace)
	skills = strings.TrimSpace(skills)
	orchestrator = strings.TrimSpace(orchestrator)

	// Merge the two most volatile blocks into a single trailing block.
	// Anthropic allows up to 4 cache_control breakpoints; keeping the
	// skills+orchestrator on one boundary leaves room for core/tools/
	// workspace to each have their own stable breakpoint.
	tail := skills
	if orchestrator != "" {
		if tail != "" {
			tail += "\n\n"
		}
		tail += orchestrator
	}

	ordered := []string{core, tools, workspace, tail}

	var parts []models.ContentBlock
	var sb strings.Builder
	for _, text := range ordered {
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

	return models.Message{
		Role:        "system",
		Content:     sb.String(),
		SystemParts: parts,
	}
}
