package cli

import (
	"testing"

	"github.com/c-bata/go-prompt"
)

// Note: i18n is initialized by config_sections_test.TestMain with
// CHATCLI_LANG=en. Our assertions only check the Text field (command
// verbs), not Description, so they're locale-independent regardless.

func containsText(sugs []prompt.Suggest, text string) bool {
	for _, s := range sugs {
		if s.Text == text {
			return true
		}
	}
	return false
}

func TestReflectSubcommandMenu_ContainsAllVerbs(t *testing.T) {
	menu := reflectSubcommandMenu()
	for _, want := range []string{"list", "failed", "retry", "purge", "drain", "<lesson>"} {
		if !containsText(menu, want) {
			t.Errorf("menu missing %q: got %+v", want, menu)
		}
	}
}

func TestFilterSuggestions_EmptyPrefixReturnsAll(t *testing.T) {
	menu := reflectSubcommandMenu()
	got := filterSuggestions(menu, "")
	if len(got) != len(menu) {
		t.Fatalf("empty prefix should pass through; got %d want %d", len(got), len(menu))
	}
}

func TestFilterSuggestions_PrefixMatch(t *testing.T) {
	menu := reflectSubcommandMenu()
	got := filterSuggestions(menu, "re")
	// "re" → "retry" only
	if len(got) != 1 || got[0].Text != "retry" {
		t.Fatalf("prefix 're' should yield [retry]; got %+v", got)
	}
}

func TestFilterSuggestions_NoMatch(t *testing.T) {
	menu := reflectSubcommandMenu()
	got := filterSuggestions(menu, "zzz")
	if len(got) != 0 {
		t.Fatalf("no-match prefix should yield empty; got %+v", got)
	}
}

func TestGetReflectSuggestions_RootAlone(t *testing.T) {
	cli := &ChatCLI{}
	d := prompt.NewBuffer()
	d.InsertText("/reflect", false, true)
	sugs := cli.getReflectSuggestions(*d.Document())
	if !containsText(sugs, "/reflect") {
		t.Fatalf("expected /reflect root suggestion; got %+v", sugs)
	}
}

func TestGetReflectSuggestions_SpaceShowsMenu(t *testing.T) {
	cli := &ChatCLI{}
	d := prompt.NewBuffer()
	d.InsertText("/reflect ", false, true)
	sugs := cli.getReflectSuggestions(*d.Document())
	for _, verb := range []string{"list", "failed", "retry", "purge", "drain"} {
		if !containsText(sugs, verb) {
			t.Errorf("menu should include %q after '/reflect '; got %+v", verb, sugs)
		}
	}
}

func TestGetReflectSuggestions_PrefixNarrowsMenu(t *testing.T) {
	cli := &ChatCLI{}
	d := prompt.NewBuffer()
	d.InsertText("/reflect re", false, true)
	sugs := cli.getReflectSuggestions(*d.Document())
	if len(sugs) != 1 || sugs[0].Text != "retry" {
		t.Fatalf("prefix 're' should narrow to [retry]; got %+v", sugs)
	}
}

func TestGetReflectSuggestions_RetryWithSpaceShowsIDPlaceholder(t *testing.T) {
	cli := &ChatCLI{}
	d := prompt.NewBuffer()
	d.InsertText("/reflect retry ", false, true)
	sugs := cli.getReflectSuggestions(*d.Document())
	// Without a runner wired, falls back to placeholder.
	if !containsText(sugs, "<job-id>") {
		t.Fatalf("retry with space should offer <job-id> placeholder; got %+v", sugs)
	}
}

func TestGetReflectSuggestions_PurgeWithSpaceShowsIDPlaceholder(t *testing.T) {
	cli := &ChatCLI{}
	d := prompt.NewBuffer()
	d.InsertText("/reflect purge ", false, true)
	sugs := cli.getReflectSuggestions(*d.Document())
	if !containsText(sugs, "<job-id>") {
		t.Fatalf("purge with space should offer <job-id> placeholder; got %+v", sugs)
	}
}

func TestGetReflectSuggestions_UnrelatedTokenFallsBackToLessonHint(t *testing.T) {
	// A token that doesn't match any subcommand prefix is treated as
	// the start of free-text lesson content; completer must not leak
	// menu options there.
	cli := &ChatCLI{}
	d := prompt.NewBuffer()
	d.InsertText("/reflect when", false, true)
	sugs := cli.getReflectSuggestions(*d.Document())
	if !containsText(sugs, "<lesson>") {
		t.Fatalf("non-verb prefix should show <lesson> hint; got %+v", sugs)
	}
}
