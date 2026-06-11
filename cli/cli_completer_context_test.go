/*
 * ChatCLI - Tests for the per-subcommand /context suggestion helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Drives the /context dispatcher through docWithCursor with realistic
 * inputs and asserts on the resulting suggestion slice. Each test names
 * the subcommand it exercises so failures point at the right helper.
 */
package cli

import (
	"testing"

	prompt "github.com/c-bata/go-prompt"
)

func TestGetContextSuggestions_BareCommandShowsRootDesc(t *testing.T) {
	cli := newCompleterTestCLI(t)
	d := docWithCursor("/context", len("/context"))
	out := cli.getContextSuggestions(d)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (root description)", len(out))
	}
	if out[0].Text != "/context" {
		t.Errorf("Text = %q, want /context", out[0].Text)
	}
}

func TestGetContextSuggestions_TrailingSpaceListsSubcommands(t *testing.T) {
	cli := newCompleterTestCLI(t)
	d := docWithCursor("/context ", len("/context "))
	out := cli.getContextSuggestions(d)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Text] = true
	}
	for _, want := range []string{"create", "update", "attach", "list", "merge", "export", "import"} {
		if !seen[want] {
			t.Errorf("missing subcommand %q in /context completion", want)
		}
	}
}

func TestContextMergeSuggestions_PromptsForNewName(t *testing.T) {
	cli := newCompleterTestCLI(t)
	out := cli.contextMergeSuggestions([]string{"/context", "merge"}, true)
	if len(out) != 1 {
		t.Fatalf("merge first arg should prompt for the new name; len=%d", len(out))
	}
}

func TestContextImportSuggestions_EmptyArgsReturnsNil(t *testing.T) {
	cli := newCompleterTestCLI(t)
	if got := cli.contextImportSuggestions([]string{}, prompt.Document{}); got != nil {
		t.Errorf("import with empty args → nil; got %+v", got)
	}
}

func TestContextNameOrFlagSuggestions_AttachFlags(t *testing.T) {
	cli := newCompleterTestCLI(t)
	// `/context attach myctx --` — past the name, typing a flag → flag list.
	line := "/context attach myctx --"
	d := docWithCursor(line, len(line))
	out := cli.contextNameOrFlagSuggestions("attach", []string{"/context", "attach", "myctx", "--"}, line, d)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Text] = true
	}
	for _, want := range []string{"--priority", "--chunk", "--chunks"} {
		if !seen[want] {
			t.Errorf("expected attach flag %q; got %+v", want, out)
		}
	}
}

func TestContextNameOrFlagSuggestions_InspectChunkFlag(t *testing.T) {
	cli := newCompleterTestCLI(t)
	line := "/context inspect myctx --"
	d := docWithCursor(line, len(line))
	out := cli.contextNameOrFlagSuggestions("inspect", []string{"/context", "inspect", "myctx", "--"}, line, d)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Text] = true
	}
	if !seen["--chunk"] || !seen["-c"] {
		t.Errorf("inspect --<TAB> should offer --chunk / -c; got %+v", out)
	}
}

func TestContextCreateOrUpdateSuggestions_FlagPrefixOffersFlagList(t *testing.T) {
	cli := newCompleterTestCLI(t)
	line := "/context create myctx --"
	d := docWithCursor(line, len(line))
	out := cli.contextCreateOrUpdateSuggestions("create",
		[]string{"/context", "create", "myctx", "--"}, line, d)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Text] = true
	}
	for _, want := range []string{"--mode", "--description", "--tags", "--force"} {
		if !seen[want] {
			t.Errorf("expected flag %q in create/update completion; got %+v", want, out)
		}
	}
}

// TestContextModeValueSuggestions_ListsEveryProcessingMode drives the
// create-path completer to the position right after `--mode` and asserts
// every mode the handler's validator accepts is offered — including
// `knowledge`, which was missing from the value list (the flag's own
// description already advertised it). The palette overlay reuses this same
// completer output, so the gate here covers both surfaces.
func TestContextModeValueSuggestions_ListsEveryProcessingMode(t *testing.T) {
	cli := newCompleterTestCLI(t)
	line := "/context create myctx ./src --mode "
	d := docWithCursor(line, len(line))
	out := cli.contextCreateOrUpdateSuggestions("create",
		[]string{"/context", "create", "myctx", "./src", "--mode"}, line, d)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Text] = true
	}
	for _, want := range []string{"full", "summary", "chunked", "smart", "knowledge"} {
		if !seen[want] {
			t.Errorf("expected mode %q after --mode; got %+v", want, out)
		}
	}
}

func TestContextExportSuggestions_AfterNameSuggestsPath(t *testing.T) {
	cli := newCompleterTestCLI(t)
	line := "/context export myctx "
	d := docWithCursor(line, len(line))
	out := cli.contextExportSuggestions(
		[]string{"/context", "export", "myctx"}, true, d,
	)
	// Result depends on cwd — but the slice should be non-nil because
	// the path-completion branch fired.
	if out == nil {
		t.Error("export past the name should at least invoke the file path completer")
	}
}

func TestSuggestPreferArgs_OffersResetAndLocal(t *testing.T) {
	cli := newCompleterTestCLI(t)
	// 4 tokens with trailing space so FilterHasPrefix sees the empty
	// word-before-cursor and keeps every suggestion.
	line := "/skill prefer foo bar "
	d := docWithCursor(line, len(line))
	out := cli.suggestPreferArgs(d)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Text] = true
	}
	if !seen["--reset"] {
		t.Errorf("expected --reset in suggestions; got %+v", out)
	}
	if !seen["local"] {
		t.Errorf("expected 'local' in suggestions; got %+v", out)
	}
}
