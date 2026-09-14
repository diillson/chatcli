/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The shape of the memory extraction request, chosen for the prompt cache.
 *
 * The extraction ran every few minutes with one user message that held
 * the instructions, the workspace line, the whole existing long-term
 * memory and the new conversation segment — and the instructions once
 * more as a system message. On a provider with a prefix cache the rolling
 * breakpoint sat on that user message, so every call wrote the entire
 * prompt to the cache and read nothing back, because the segment at the
 * end differed each time; the instructions were paid for twice. Measured
 * on a real session the worker was a third of the bill.
 *
 * The request is now split by lifetime, like the conversation prompts
 * are. The instructions are the first system block and never change
 * within a session. The workspace line and the existing memory are the
 * second block: stable between extractions, rewritten only when an
 * extraction added something. The segment alone is the user message.
 * Each system block carries a cache hint, so on a cached provider the
 * three-minute cadence reads the instructions and the memory back from
 * a warm entry and pays the write for the segment only; on every other
 * provider the same blocks travel as one system string, as before, minus
 * the duplicate.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/models"
)

// extractionSegmentHeader introduces the segment in the user message.
const extractionSegmentHeader = "CONVERSATION SEGMENT TO ANALYZE:\n\n"

// buildExtractionRequest returns the prompt (the user message text) and
// the history the extraction call sends: a system message split into
// cached blocks and the segment as the only user message.
func buildExtractionRequest(instructions, workspaceDir, existingContext, segment string) (string, []models.Message) {
	var context strings.Builder
	if workspaceDir != "" {
		// Model-facing prompt text, English on purpose like the extraction
		// instructions it accompanies.
		context.WriteString("CURRENT SESSION WORKSPACE: " + workspaceDir + "\n")
		context.WriteString("(All paths and facts from this conversation belong to this workspace.)")
	}
	if existingContext != "" {
		if context.Len() > 0 {
			context.WriteString("\n\n---\n\n")
		}
		context.WriteString(existingContext)
	}

	parts := []models.ContentBlock{{
		Type:         "text",
		Text:         instructions,
		CacheControl: &models.CacheControl{Type: "ephemeral"},
	}}
	flat := instructions
	if context.Len() > 0 {
		parts = append(parts, models.ContentBlock{
			Type:         "text",
			Text:         context.String(),
			CacheControl: &models.CacheControl{Type: "ephemeral"},
		})
		flat += "\n\n---\n\n" + context.String()
	}

	prompt := extractionSegmentHeader + segment
	history := []models.Message{
		{Role: "system", Content: flat, SystemParts: parts},
		{Role: "user", Content: prompt},
	}
	return prompt, history
}
