/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"regexp"
	"strings"

	"github.com/diillson/chatcli/cli/agent/workers"
)

// shellPromptLineRe matches a line that starts with a shell prompt marker
// ("$ cmd" / "# cmd") — the signature of the model dumping raw shell
// commands instead of emitting tool calls.
var shellPromptLineRe = regexp.MustCompile(`(?m)^[$#]\s+`)

// looksLikeLooseCode reports whether a coder-mode response that carried no
// tool calls is dumping raw code blocks or shell commands instead of using
// the tool-call protocol.
//
// Responses carrying <agent_call> tags are exempt: their task attributes
// legitimately embed code — Python comments start lines with "# ", markdown
// fences appear in instructions — and the squad dispatch block is the
// intended consumer of that text. Flagging them here used to reject the
// whole response with a FORMAT ERROR before the dispatch ever ran, silently
// dropping valid dispatches and steering the model away from the squad flow
// (the corrective message says "use <tool_call>"). Malformed agent_call tags
// remain covered by the dispatch block's own CountAgentCallTags feedback.
func looksLikeLooseCode(aiResponse string) bool {
	if workers.CountAgentCallTags(aiResponse) > 0 {
		return false
	}
	return strings.Contains(aiResponse, "```") || shellPromptLineRe.MatchString(aiResponse)
}
