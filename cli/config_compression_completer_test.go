/*
 * ChatCLI - completer coverage for /config compression prune.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The interactive command palette is completer-driven (palette_bridge.go runs
 * cli.completer), so asserting the suggestion here also guarantees `prune`
 * shows up — and autocompletes — in the palette overlay.
 */
package cli

import "testing"

func hasSuggestion(cli *ChatCLI, line string, want string) bool {
	for _, s := range cli.getConfigCompressionSuggestions(docWithCursor(line, len(line))) {
		if s.Text == want {
			return true
		}
	}
	return false
}

func TestConfigCompressionCompleter_OffersPrune(t *testing.T) {
	cli := &ChatCLI{}
	// Bare subcommand position: the full set is offered (palette entry point).
	if !hasSuggestion(cli, "/config compression ", "prune") {
		t.Fatal("`prune` not offered at the /config compression subcommand position")
	}
	// Prefix typing narrows to it.
	if !hasSuggestion(cli, "/config compression pr", "prune") {
		t.Fatal("`prune` not offered when typing the `pr` prefix")
	}
}
