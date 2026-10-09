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

// coderHandoffInstruction follows the chat mode banner on attended REPL
// turns. English on purpose: models follow it more reliably. It names the
// shapes a request takes when it needs the workspace — a bare command, a
// question whose answer needs a run — because a short imperative such as
// "execute ls" is exactly where models fell back to "use /coder".
const coderHandoffInstruction = `
**CODER HANDOFF — how you hand work to coder mode:** in chat you cannot read or modify files, run commands or run tests. Whenever a proper answer needs any of that — a request phrased as a command ("execute ls", "run the tests", "rode os testes", "crie o arquivo x.go"), a change to the code, running or debugging the project, or a question whose answer depends on running something or reading the files — never stop at "I can't do that here" and never tell the user to type /coder. Write one or two sentences with your understanding of the task, then end the reply with exactly one line:
<coder_handoff>concise imperative task for the coder, in the user's language</coder_handoff>
The CLI shows that line as a proposal and coder mode starts only if the user confirms. Do not emit it for questions you can fully answer from the conversation, for explanations, or for code you only show as an example.`

// chatModeRedirectRule is rule 3 of ChatModeSystemHint: send the user to
// type /coder. It is right where no confirmation is possible (one-shot,
// gateway, MCP/ACP), and wrong on an attended REPL turn, where it
// contradicted the handoff instruction and models followed it instead.
const chatModeRedirectRule = `3. If the user asks you to run a command, modify a file, or perform any action that requires execution, politely point them to /coder mode (e.g. "To execute that, please use /coder <your request>."). /coder is THE mode to recommend for any task that needs tools or execution — do not suggest /agent unless the user explicitly asks about it.`

// chatModeHandoffRule replaces chatModeRedirectRule on attended REPL turns.
const chatModeHandoffRule = `3. If the user asks you to run a command (even a single one such as ls or go test), read, create or modify files, run or debug the project, or do any other work in the workspace, do NOT refuse and do NOT tell the user to type /coder: propose the switch with the CODER HANDOFF line described below, and the CLI asks the user to confirm before coder mode starts. That line is not command-execution syntax; it is the proposal. Do not suggest /agent unless the user explicitly asks about it.`

// chatModeBanner is the chat mode banner for this turn: ChatModeSystemHint
// as is, or, when the user can confirm a handoff, with its redirect rule
// replaced by the handoff rule so the two never disagree.
func chatModeBanner(handoff bool) string {
	if !handoff {
		return ChatModeSystemHint
	}
	return strings.Replace(ChatModeSystemHint, chatModeRedirectRule, chatModeHandoffRule, 1)
}

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

// takeCoderHandoff strips a handoff proposal from a reply that is about to
// be rendered and stored. A reply that was only the tag becomes a short
// note, so the transcript never holds an empty assistant turn. Shared by
// the chat turn and /moa, whose panel gets the same chat briefing.
func takeCoderHandoff(reply string) (task, cleaned string) {
	task, cleaned = extractCoderHandoff(reply)
	if task != "" && cleaned == "" {
		cleaned = i18n.T("handoff.only_tag")
	}
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

// offerCoderHandoff shows a proposal taken from a reply, when there is one
// and this turn can be confirmed.
func (cli *ChatCLI) offerCoderHandoff(task string) {
	if task != "" && cli.coderHandoffActive() {
		cli.noteCoderHandoff(task)
	}
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
