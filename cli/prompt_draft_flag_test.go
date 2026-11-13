/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import "testing"

// A draft is offered while a turn runs from the prompt's executor, and the
// typed line then leaves the prompt by a mode switch (/coder): nothing
// consumed the rebuild request. The next prompt is built with the draft;
// its first keystroke must not tear it down — the draft was already taken
// and would be gone.
func TestPromptDraftSurvivesAModeSwitch(t *testing.T) {
	cli := &ChatCLI{promptRunning: true}
	cli.offerPromptDraft("y")
	if cli.themeReloadPending {
		t.Fatal("a draft must not borrow the theme-reload flag: nothing clears it on a mode switch")
	}
	// The executor's line leaves p.Run by the mode-switch panic; the turn
	// runs; the loop builds the next prompt.
	cli.promptRunning = false
	if got := cli.promptInitialText(); got != "y" {
		t.Fatalf("the next prompt must start with the draft, got %q", got)
	}
	cli.promptRunning = true
	if cli.promptShouldRebuild("x", false) {
		t.Fatal("the first keystroke rebuilt the prompt and lost the draft it was showing")
	}
	if got := cli.promptInitialText(); got != "" {
		t.Fatalf("a draft the user started editing is not offered again, got %q", got)
	}
}

// A draft stays pending until a prompt that showed it has started reading
// input: a prompt rebuilt before any input (a theme change landing at the
// same time) shows it again. Offered while the prompt runs, it asks that
// prompt — and only once — to rebuild.
func TestPromptDraftIsKeptUntilAPromptReadsInput(t *testing.T) {
	cli := &ChatCLI{}
	cli.offerPromptDraft("y")
	first := cli.promptInitialText()
	if again := cli.promptInitialText(); first != "y" || again != "y" {
		t.Fatalf("a prompt torn down before reading input must not take the draft with it: %q then %q", first, again)
	}
	cli.promptRunning = true
	if cli.promptShouldRebuild("", true) {
		t.Fatal("showing a draft is not a reason to rebuild")
	}
	if cli.promptInitialText() != "" {
		t.Fatal("once a prompt read input with the draft on its line, the draft is spent")
	}

	// Offered under a running prompt: one rebuild, which then shows it.
	cli.offerPromptDraft("n")
	if !cli.promptShouldRebuild("/resume tok", true) {
		t.Fatal("a draft offered while the prompt runs must make it rebuild after the executor returns")
	}
	if !cli.takePromptRebuild() || cli.takePromptRebuild() {
		t.Fatal("the rebuild request is consumed once, by the loop that rebuilds")
	}
	if cli.promptInitialText() != "n" || cli.promptShouldRebuild("a", false) {
		t.Fatal("the rebuilt prompt shows the draft and keeps running")
	}
	// The theme flag keeps its own meaning.
	cli.themeReloadPending = true
	if !cli.promptShouldRebuild("a", false) || cli.takePromptRebuild() {
		t.Fatal("a theme change still rebuilds, and is not a draft request")
	}
}
