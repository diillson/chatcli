/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * agent_context_block.go
 *
 * Injects the session's `/context attach` entries into the agent/coder system
 * prompt — the agent-mode counterpart of the chat pipeline's Part 1
 * (attachedContextParts). Historically these attachments were chat-only,
 * which broke the "same experience" promise for /agent, /coder and every
 * headless surface built on the loop (gateway, MCP server): a context the
 * user attached in chat silently vanished the moment a task ran.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"go.uber.org/zap"
)

// attachedContextAgentBlock renders every `/context attach`ed entry for the
// current session as one prompt block. Session-stable (changes only on
// attach/detach), so it belongs in the agent system prompt's cacheable
// region, next to the knowledge index cards. Returns "" when the session has
// no attachments — knowledge bases are excluded here because they already
// ride as index cards via knowledgeAgentBlock.
func (cli *ChatCLI) attachedContextAgentBlock() string {
	if cli.contextHandler == nil {
		return ""
	}
	sessionID := cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}
	msgs, err := cli.contextHandler.GetManager().BuildPromptMessages(
		sessionID,
		ctxmgr.FormatOptions{
			IncludeMetadata:  true,
			IncludeTimestamp: false,
			Compact:          false,
			Role:             "system",
		},
	)
	if err != nil {
		cli.logger.Warn("agent: failed to build attached-context block", zap.Error(err))
		return ""
	}
	if len(msgs) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, msg := range msgs {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(msg.Content)
	}
	return sb.String()
}
