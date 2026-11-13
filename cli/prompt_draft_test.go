/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

// endTurnWith ends a turn through stopStdinReader, the real turn-end path,
// with lines left on the reader's channel and a half-typed line.
func endTurnWith(t *testing.T, cli *ChatCLI, promptOpen bool, typing string, lines ...string) (a *AgentMode, out string) {
	t.Helper()
	a = &AgentMode{cli: cli, stdinDepth: 1}
	a.stdinLines, a.stdinDone = make(chan string, len(lines)+1), make(chan struct{})
	if promptOpen {
		defer a.noteDetachedPrompt()()
	}
	for _, line := range lines {
		a.stdinLines <- line
	}
	a.setTypeaheadPreview(typing)
	out = captureStdout(t, a.stopStdinReader)
	return a, out
}

// The turn ends — the common case — while a permission prompt of a
// detached run is waiting. What the user typed for it is announced as not
// sent AND handed to the next REPL prompt as its initial text, with the
// half-typed line after it; it is not submitted, and not offered twice.
func TestWithdrawnLineBecomesTheNextPromptText(t *testing.T) {
	cli := &ChatCLI{}
	a, out := endTurnWith(t, cli, true, "and th", "y\x1b[2J")
	if !strings.Contains(out, i18n.T("leftovers.prompt.withdrawn")) {
		t.Fatalf("the user must be told the prompt was withdrawn: %q", out)
	}
	if got := a.takeSalvagedTypeahead(); len(got) != 0 {
		t.Fatalf("the line was queued for the model: %q", got)
	}
	if got := cli.takePromptDraft(); got != "y and th" {
		t.Fatalf("the next prompt must start with what was typed, cleaned of the escape sequence in it; got %q", got)
	}
	if got := cli.takePromptDraft(); got != "" {
		t.Fatalf("a draft is offered once, got %q again", got)
	}

	// While go-prompt owns the terminal (a resume run from its executor),
	// the prompt is asked to rebuild so the draft can be shown at all.
	cli.promptRunning = true
	endTurnWith(t, cli, true, "", "n")
	if !cli.themeReloadPending || cli.takePromptDraft() != "n" {
		t.Fatal("a draft offered while the prompt runs must request its rebuild")
	}

	// No prompt was withdrawn: a turn's leftover type-ahead is reported as
	// before, and nothing is put on the next prompt.
	cli = &ChatCLI{}
	if _, out = endTurnWith(t, cli, false, "half", "also run the linter"); !strings.Contains(out, "also run the linter") || cli.takePromptDraft() != "" {
		t.Fatalf("plain type-ahead must keep its own path: %q", out)
	}
	// A surface with no prompt of its own takes no draft.
	cli = &ChatCLI{unattended: true}
	if endTurnWith(t, cli, true, "", "y"); cli.takePromptDraft() != "" {
		t.Fatal("an unattended surface has no next prompt to prefill")
	}
}
