/*
 * ChatCLI - tests for /config ui|theme completion
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// suggestTexts is defined in scheduler_completer_test.go (same package).

// TestConfigCompleter_ThemeAlias verifies that `/config theme <TAB>` (the
// alias) and `/config ui theme <TAB>` both surface the registry theme names.
func TestConfigCompleter_ThemeAlias(t *testing.T) {
	cli := newCompleterTestCLI(t)

	alias := "/config theme "
	got := suggestTexts(cli.getConfigSuggestions(docWithCursor(alias, len(alias))))
	assert.Contains(t, got, "dark", "/config theme <TAB> offers dark")
	assert.Contains(t, got, "light", "/config theme <TAB> offers light")

	full := "/config ui theme "
	got2 := suggestTexts(cli.getConfigSuggestions(docWithCursor(full, len(full))))
	assert.Contains(t, got2, "dark", "/config ui theme <TAB> offers dark")
	assert.Contains(t, got2, "light", "/config ui theme <TAB> offers light")
}

// TestConfigCompleter_UISection confirms `/config ui <TAB>` offers the theme
// subcommand.
func TestConfigCompleter_UISection(t *testing.T) {
	cli := newCompleterTestCLI(t)
	line := "/config ui "
	got := suggestTexts(cli.getConfigSuggestions(docWithCursor(line, len(line))))
	assert.Contains(t, got, "theme", "/config ui <TAB> offers the theme subcommand")
}
