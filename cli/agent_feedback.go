/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
)

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
