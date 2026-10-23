/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
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

// maxLeakedMarkupCorrections bounds how many times in a row a coder-mode run
// answers a response that leaked tool-call markup with a correction before
// letting the response stand. A model that cannot produce the tool-call
// format after a few reminders is not helped by more of them.
const maxLeakedMarkupCorrections = 3

var (
	// leakedOpenRe matches the opening tag of a function-call block in the
	// format some models fall back to when they have seen it in training:
	// <invoke name="..."> / <parameter name="...">, with or without a
	// namespace prefix.
	leakedOpenRe = regexp.MustCompile(`<(?:[a-z]+:)?(?:invoke|parameter)\s+name\s*=\s*"([^"]*)"`)
	// leakedCloseRe matches the closers of those blocks.
	leakedCloseRe = regexp.MustCompile(`</(?:[a-z]+:)?(?:invoke|parameter|function_calls)>`)
	fencedCodeRe  = regexp.MustCompile("(?s)```.*?```")
	inlineCodeRe  = regexp.MustCompile("`[^`\n]*`")
)

// leakedMarkupOutsideCode returns the response without fenced code and inline
// code spans, where tool-call markup is being discussed, not emitted.
func leakedMarkupOutsideCode(aiResponse string) string {
	return inlineCodeRe.ReplaceAllString(fencedCodeRe.ReplaceAllString(aiResponse, " "), " ")
}

// looksLikeLeakedToolMarkup reports whether a coder-mode response that
// carried no tool calls contains function-call markup in a format chatcli does
// not execute: an <invoke name="..."> or <parameter name="..."> opening, or
// at least two stray closers (the degenerate form is a response that is
// nothing but repeated </invoke>). Such a response used to end the task: the
// loop saw no tool call, treated the text as the final answer and printed
// "Task completed" with the action never run.
//
// Markup inside code fences or inline code is exempt, since an answer that
// explains the format is not an attempt to call a tool.
func looksLikeLeakedToolMarkup(aiResponse string) bool {
	t := leakedMarkupOutsideCode(aiResponse)
	if leakedOpenRe.MatchString(t) {
		return true
	}
	return len(leakedCloseRe.FindAllString(t, -1)) >= 2
}

// leakedToolMarkupFeedback is the model-facing correction for a response that
// leaked tool-call markup. It states that nothing ran, names the tool the
// markup pointed at when there is one, and shows the format that executes.
// The markup is deliberately not converted into a call: text that merely
// looks like a call (quoted from a file or a tool result, say) must never run
// because it appeared in a response.
func leakedToolMarkupFeedback(aiResponse string) string {
	var b strings.Builder
	b.WriteString("FORMAT ERROR: your last message contained function-call markup (invoke/parameter tags) that is NOT the tool-call format. ")
	b.WriteString("Nothing was executed: no command ran and no file was written. ")
	if m := leakedOpenRe.FindStringSubmatch(leakedMarkupOutsideCode(aiResponse)); m != nil && m[1] != "" {
		fmt.Fprintf(&b, "The markup named %q; ", m[1])
	}
	b.WriteString("send the action again as a <tool_call> tag, for example: ")
	b.WriteString("<tool_call name=\"@coder\" args='{\"cmd\":\"exec\",\"args\":{\"cmd\":\"your command\"}}' />")
	return b.String()
}
