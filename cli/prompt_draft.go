/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import "strings"

// offerPromptDraft hands text to the next REPL prompt as its initial,
// editable input — never as a submitted line. A turn that ends takes its
// stdin reader down, and what the user had typed for it has nowhere else
// to go: the line they typed for a permission prompt that was withdrawn
// was announced as "not sent" and then dropped. A surface with no prompt
// of its own takes none.
//
// go-prompt reads its initial text only when it is built. A turn run from
// its executor (a resume that fired while idle at the prompt) returns into
// the SAME prompt, so that prompt is asked to rebuild — the mechanism a
// theme change mid-prompt already uses.
func (cli *ChatCLI) offerPromptDraft(text string) {
	text = strings.TrimSpace(sanitizeTypeaheadPreview(text))
	if text == "" || cli.unattended {
		return
	}
	cli.promptDraftMu.Lock()
	cli.promptDraft = strings.TrimSpace(cli.promptDraft + " " + text)
	cli.promptDraftMu.Unlock()
	if cli.promptRunning {
		cli.themeReloadPending = true
	}
}

// takePromptDraft returns the pending draft, once. The REPL calls it for
// the initial buffer of each prompt it builds.
func (cli *ChatCLI) takePromptDraft() string {
	cli.promptDraftMu.Lock()
	defer cli.promptDraftMu.Unlock()
	draft := cli.promptDraft
	cli.promptDraft = ""
	return draft
}
