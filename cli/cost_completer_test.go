/*
 * ChatCLI - /cost completer and palette wiring tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"
)

// TestCostSuggestionsListSubcommands pins the /cost subcommand surface in
// the inline completer (which is also the palette's source of truth).
func TestCostSuggestionsListSubcommands(t *testing.T) {
	cli := &ChatCLI{}
	d := docWithCursor("/cost ", len("/cost "))
	suggs := cli.getCostSuggestions(d)

	want := map[string]bool{"reset": false, "last": false, "sessions": false, "export": false}
	for _, s := range suggs {
		if _, ok := want[s.Text]; ok {
			want[s.Text] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("/cost completer missing subcommand %q (got %+v)", name, suggs)
		}
	}

	// Prefix filtering works: "/cost re" narrows to reset.
	d = docWithCursor("/cost re", len("/cost re"))
	suggs = cli.getCostSuggestions(d)
	if len(suggs) != 1 || suggs[0].Text != "reset" {
		t.Errorf("prefix filter: got %+v, want only reset", suggs)
	}

	// With a complete subcommand typed there is nothing more to offer.
	d = docWithCursor("/cost reset ", len("/cost reset "))
	if suggs = cli.getCostSuggestions(d); len(suggs) != 0 {
		t.Errorf("no suggestions expected after a subcommand, got %+v", suggs)
	}
}

// TestCostStaysDirectRunInPalette: bare /cost must execute the summary, not
// be hijacked by the per-command palette overlay now that it has completable
// subcommands.
func TestCostStaysDirectRunInPalette(t *testing.T) {
	if !paletteDirectRun["/cost"] {
		t.Fatal("/cost missing from paletteDirectRun — a bare /cost would open the palette instead of the summary")
	}
}
