/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * coder_handoff.go
 *
 * Chat → coder handoff. Chat mode is tool-less by design: when a request
 * needs the workspace (editing files, running commands or tests, multi-step
 * work) the model used to answer "use /coder". Now it proposes the switch
 * itself: the reply ends with a <coder_handoff> line carrying a concise task,
 * the CLI strips the line, shows the proposal and waits for the user's next
 * input. Enter or "y" accepts and enters coder mode with that task through
 * the same path a typed /coder takes; anything else stays in chat. Plain text
 * never changes mode on its own — the user always confirms.
 *
 * The instruction only reaches the model on an attended REPL turn; headless
 * surfaces (gateway, MCP/ACP, -p) never see it and strip a stray tag.
 */
package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// coderHandoffEnv toggles the handoff. Unset means enabled.
const coderHandoffEnv = "CHATCLI_CHAT_CODER_HANDOFF"

// coderHandoffTag is the element the model emits; parsed case-insensitively.
const coderHandoffTag = "coder_handoff"

// coderHandoffMaxTask bounds the task the proposal carries.
const coderHandoffMaxTask = 600

// coderHandoffInstruction is appended to the chat system prompt on attended
// REPL turns. English on purpose: models follow it more reliably.
const coderHandoffInstruction = `
**CODER HANDOFF:** you cannot read or modify files, run commands or tests here. When the user's request needs that (changing code, running or debugging the project, multi-step work in the workspace), do not pretend to do it and do not tell the user to type /coder: answer briefly with your understanding of the task, then end your reply with exactly one line ` + "`<coder_handoff>concise imperative task for the coder</coder_handoff>`" + `. The CLI shows the proposal and the user confirms before coder mode starts. Never emit the tag for questions you can answer directly.`

var coderHandoffRe = regexp.MustCompile(`(?is)<\s*` + coderHandoffTag + `\s*>(.*?)<\s*/\s*` + coderHandoffTag + `\s*>`)

// coderHandoffEnabled reads CHATCLI_CHAT_CODER_HANDOFF; only an explicit
// false/0/off/no disables it.
func coderHandoffEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(coderHandoffEnv))) {
	case "false", "0", "off", "no":
		return false
	}
	return true
}

// coderHandoffActive reports whether this process can act on a proposal:
// an attended interactive REPL with the feature enabled. Headless surfaces
// have nobody to confirm, so the model is never asked to propose.
func (cli *ChatCLI) coderHandoffActive() bool {
	return cli.replActive && !cli.unattended && coderHandoffEnabled()
}

// extractCoderHandoff returns the proposed task and the reply with every
// handoff tag removed. task is "" when the reply carries none. Whitespace
// inside the task is collapsed and the task is bounded.
func extractCoderHandoff(text string) (task, cleaned string) {
	matches := coderHandoffRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return "", text
	}
	for _, m := range matches {
		if t := strings.Join(strings.Fields(m[1]), " "); t != "" && task == "" {
			task = t
		}
	}
	if len(task) > coderHandoffMaxTask {
		task = strings.TrimSpace(task[:coderHandoffMaxTask])
	}
	cleaned = strings.TrimSpace(coderHandoffRe.ReplaceAllString(text, ""))
	return task, cleaned
}

// handoffAnswer classifies the user's next input while a proposal waits.
type handoffAnswer int

const (
	handoffAccept  handoffAnswer = iota // enter coder mode with the task
	handoffDecline                      // stay in chat, nothing else to do
	handoffOther                        // stay in chat and treat the input as a normal turn
)

// classifyHandoffAnswer: an empty line (Enter) or a yes word accepts; a no
// word declines; any other text is a regular chat turn that implicitly
// declines.
func classifyHandoffAnswer(in string) handoffAnswer {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "", "y", "yes", "s", "sim", "ok":
		return handoffAccept
	case "n", "no", "nao", "não":
		return handoffDecline
	}
	return handoffOther
}

// noteCoderHandoff records the proposal and shows it under the reply.
func (cli *ChatCLI) noteCoderHandoff(task string) {
	cli.pendingCoderHandoff = task
	fmt.Println()
	fmt.Println(colorize("  🛠  "+i18n.T("handoff.proposal_title"), ColorCyan))
	fmt.Println(colorize("     "+task, ColorBold))
	fmt.Println(colorize("     "+i18n.T("handoff.proposal_hint"), ColorYellow))
}

// consumeCoderHandoffAnswer runs at the top of the REPL executor. With no
// proposal pending it does nothing. Otherwise it consumes the proposal and
// returns the input the executor should process: the /coder invocation on
// accept (handled=true), "" on decline (handled=true, nothing to run), or the
// original text when the user simply kept chatting (handled=false).
func (cli *ChatCLI) consumeCoderHandoffAnswer(in string) (string, bool) {
	task := cli.pendingCoderHandoff
	if task == "" {
		return in, false
	}
	cli.pendingCoderHandoff = ""
	switch classifyHandoffAnswer(in) {
	case handoffAccept:
		fmt.Println(colorize("  "+i18n.T("handoff.accepted"), ColorGreen))
		return "/coder " + task, true
	case handoffDecline:
		fmt.Println(colorize("  "+i18n.T("handoff.declined"), ColorYellow))
		return "", true
	default:
		fmt.Println(colorize("  "+i18n.T("handoff.declined"), ColorYellow))
		return in, false
	}
}
