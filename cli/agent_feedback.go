/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
)

// toolCallNamesLabel names a batch for the model-facing feedback message
// using the REAL tools invoked ("@coder", "@coder, @file"). The label lands
// verbatim inside "The tool '%s' was executed…", and models happily narrate
// it back to the user — so it must never be an internal placeholder.
func toolCallNamesLabel(toolCalls []agent.ToolCall) string {
	seen := map[string]bool{}
	names := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		name := strings.TrimSpace(tc.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		return "@coder"
	}
	return strings.Join(names, ", ")
}

// commandBlockNamesLabel names an ```execute``` block batch the same way,
// from the block languages ("shell", "shell, git").
func commandBlockNamesLabel(blocks []CommandBlock) string {
	seen := map[string]bool{}
	names := make([]string, 0, len(blocks))
	for _, b := range blocks {
		lang := strings.TrimSpace(b.Language)
		if lang == "" {
			lang = "shell"
		}
		if seen[lang] {
			continue
		}
		seen[lang] = true
		names = append(names, lang)
	}
	if len(names) == 0 {
		return "shell"
	}
	return strings.Join(names, ", ")
}

// batchContainsRecall reports whether any tool call in a batch invoked @recall.
// Recall returns a previously-compressed original verbatim, so a batch that
// includes it produces feedback that must survive history compaction intact.
func batchContainsRecall(toolCalls []agent.ToolCall) bool {
	for _, tc := range toolCalls {
		if plugins.IsRecallTool(tc.Name) {
			return true
		}
	}
	return false
}

// buildBatchFeedbackMessage wraps a batch's textual tool output into the user
// message fed back to the model. When the batch invoked @recall, the message is
// flagged PreserveVerbatim so history compaction never re-reduces it — the
// model explicitly asked to see that original in full, and trimming it would
// force another recall. The flag is structural, so the trimmer needs no
// knowledge of the tool-output text format.
func buildBatchFeedbackMessage(content string, toolCalls []agent.ToolCall) models.Message {
	msg := models.Message{Role: "user", Content: content}
	if batchContainsRecall(toolCalls) {
		msg.Meta = &models.MessageMeta{PreserveVerbatim: true}
	}
	return msg
}
