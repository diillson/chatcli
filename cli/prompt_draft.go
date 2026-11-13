/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"sync"
)

// promptDraftState carries text from a finished turn to the next REPL
// prompt as its initial, editable input — never as a submitted line. A
// turn that ends takes its stdin reader down, and what the user had typed
// for it has nowhere else to go: the line typed for a permission prompt
// that was withdrawn was announced as "not sent" and then dropped.
//
// go-prompt reads its initial text only when it is built, so the draft has
// a small life cycle of its own:
//
//   - offered: stored; if a prompt is running (the turn ran from its
//     executor) that prompt is asked to rebuild — with a flag of the
//     draft's own. It used to borrow the theme-reload flag, which nothing
//     clears when the typed line leaves the prompt by a mode switch: the
//     next prompt was built with the draft, its first keystroke tripped the
//     stale flag, and the rebuilt prompt had no draft left to show.
//   - shown: a prompt was built with it. It is still pending — that prompt
//     may be torn down before the user sees it.
//   - spent: a prompt that showed it has read input. Only then is it
//     cleared.
type promptDraftState struct {
	mu      sync.Mutex
	text    string
	shown   bool
	rebuild bool
	// onLine is the draft the running prompt was built with, until the
	// executor has taken a line from that prompt. It outlives "spent": the
	// keystrokes that spend a draft may be ones nobody typed.
	onLine string
}

// offerPromptDraft hands text to the next REPL prompt. A surface with no
// prompt of its own takes none.
func (cli *ChatCLI) offerPromptDraft(text string) {
	text = strings.TrimSpace(sanitizeTypeaheadPreview(text))
	if text == "" || cli.unattended {
		return
	}
	d := &cli.promptDraft
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.text != text { // offering back what is still pending adds nothing
		d.text = strings.TrimSpace(d.text + " " + text)
	}
	d.shown = false
	if cli.promptRunning {
		d.rebuild = true
	}
}

// promptInitialText is the text the prompt being built starts with. The
// build is also what a rebuild request asked for.
func (cli *ChatCLI) promptInitialText() string {
	d := &cli.promptDraft
	d.mu.Lock()
	defer d.mu.Unlock()
	d.rebuild = false
	d.shown = d.text != ""
	d.onLine = d.text
	return d.text
}

// undraftInjectedResume is called with every line the prompt submits. When
// a park fires while the terminal idles, the bridge types "/resume <token>"
// and Enter into the terminal; with a draft on the input line the result
// is the draft glued to that command, which is no command at all and went
// to the model as the user's message — with nobody at the keyboard. A line
// that is exactly the draft this prompt was showing, followed by the
// resume of a token that IS queued, is that resume: the draft is offered
// again, unchanged. Any other line is the user's and is returned as it is.
func (cli *ChatCLI) undraftInjectedResume(line string) string {
	d := &cli.promptDraft
	d.mu.Lock()
	shown := d.onLine
	d.onLine = ""
	d.mu.Unlock()
	rest, ok := strings.CutPrefix(line, shown)
	if shown == "" || !ok {
		return line
	}
	token, isResume := parsePendingResumeLine(strings.TrimSpace(rest))
	if !isResume || !cli.hasPendingResume(token) {
		return line
	}
	cli.offerPromptDraft(shown)
	return "/resume " + token
}

// promptShouldRebuild is the prompt's exit check, called by go-prompt on
// every input event: whether it must be torn down and built again. Being
// called at all means the running prompt is reading input, which spends a
// draft it was showing.
func (cli *ChatCLI) promptShouldRebuild(string, bool) bool {
	d := &cli.promptDraft
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shown {
		d.text, d.shown = "", false
	}
	return cli.themeReloadPending || d.rebuild
}

// takePromptRebuild reports, once, that a draft asked for a prompt
// rebuild. The REPL loop calls it when the prompt has exited.
func (cli *ChatCLI) takePromptRebuild() bool {
	d := &cli.promptDraft
	d.mu.Lock()
	defer d.mu.Unlock()
	asked := d.rebuild
	d.rebuild = false
	return asked
}
