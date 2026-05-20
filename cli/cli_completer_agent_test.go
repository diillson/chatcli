/*
 * ChatCLI - getConfigAgentSuggestions tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"

	prompt "github.com/c-bata/go-prompt"
	"github.com/stretchr/testify/assert"
)

// docAt builds a Document whose cursor sits at the end of `text`. The
// public Document literal sets only Text and leaves cursorPosition at
// zero, which gives TextBeforeCursor() = "" — useless for completer
// tests because every dispatch keys off "tokens before cursor".
// go-prompt's Buffer is the only public API that lets us position
// the cursor, so we route through it.
func docAt(text string) prompt.Document {
	b := prompt.NewBuffer()
	b.InsertText(text, false, true)
	return *b.Document()
}

// TestGetConfigAgentSuggestions_Subcommand exercises the slot-3 path
// (`/config agent <TAB>`) which lists ui/help. Without this, a
// regression that drops the suggestion table would only show as
// "TAB does nothing" — easy to miss without a test.
func TestGetConfigAgentSuggestions_Subcommand(t *testing.T) {
	c := &ChatCLI{}
	sugs := c.getConfigAgentSuggestions(docAt("/config agent "))

	texts := suggestionTexts(sugs)
	assert.Contains(t, texts, "ui", "slot-3 must offer the ui subcommand")
	assert.Contains(t, texts, "help", "slot-3 must offer help")
}

// TestGetConfigAgentSuggestions_UIStyles exercises slot-4: after the
// user typed `/config agent ui ` the completer must list the three
// valid style values. The PR's whole point is to make the UI
// switchable at runtime — broken completion here makes the feature
// undiscoverable.
func TestGetConfigAgentSuggestions_UIStyles(t *testing.T) {
	c := &ChatCLI{}
	sugs := c.getConfigAgentSuggestions(docAt("/config agent ui "))

	texts := suggestionTexts(sugs)
	for _, want := range []string{"full", "compact", "minimal"} {
		assert.Contains(t, texts, want,
			"ui-style slot must offer %q", want)
	}
}

// TestGetConfigAgentSuggestions_PrefixFilter proves the filter trims
// suggestions by the word currently under the cursor. Typing "co"
// should keep only "compact" — without that, the user sees every
// option even after they've started typing the one they want.
func TestGetConfigAgentSuggestions_PrefixFilter(t *testing.T) {
	c := &ChatCLI{}
	sugs := c.getConfigAgentSuggestions(docAt("/config agent ui co"))

	texts := suggestionTexts(sugs)
	assert.Equal(t, []string{"compact"}, texts,
		"prefix 'co' must narrow to compact only")
}

// TestGetConfigAgentSuggestions_BeyondLast covers the "no more
// suggestions" path: anything past slot 4 returns empty. Prevents a
// regression where the completer would spuriously re-list values
// when the user kept typing past the style token.
func TestGetConfigAgentSuggestions_BeyondLast(t *testing.T) {
	c := &ChatCLI{}
	sugs := c.getConfigAgentSuggestions(docAt("/config agent ui compact extra "))
	assert.Empty(t, sugs)
}

func suggestionTexts(s []prompt.Suggest) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, x.Text)
	}
	return out
}
