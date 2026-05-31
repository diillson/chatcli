/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package palette

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/ui/theme"
)

// fakeSuggest emulates the completer: it maps a command line to its next-token
// options, exercising static subcommands, dynamic values, flags and free-form
// argument hints.
func fakeSuggest(line string) []Suggestion {
	switch strings.TrimSpace(line) {
	case "/config":
		return sg("ui", "general", "providers")
	case "/config ui":
		return sg("theme")
	case "/config ui theme":
		return sg("dark", "light")
	// /model has a STATELESS completer: it re-offers the full model list for
	// any position after the command (word="" → FilterHasPrefix returns all),
	// even once a model is already chosen. The palette must treat a value pick
	// as terminal instead of drilling forever.
	case "/model", "/model gpt-4o", "/model claude-opus":
		return sg("gpt-4o", "claude-opus")
	case "/switch":
		return sg("--model", "--max-tokens")
	case "/switch --model", "/switch --model gpt-4o", "/switch --model claude-opus":
		return sg("gpt-4o", "claude-opus")
	case "/lsp":
		return []Suggestion{{Text: "<path>"}}
	}
	return nil
}

func sg(tokens ...string) []Suggestion {
	out := make([]Suggestion, len(tokens))
	for i, t := range tokens {
		out[i] = Suggestion{Text: t, Desc: "desc " + t}
	}
	return out
}

func key(t *testing.T, m Model, msg tea.KeyMsg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return mm
}

func typeRunes(t *testing.T, m Model, s string) Model {
	t.Helper()
	return key(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

func moveTo(t *testing.T, m Model, token string) Model {
	t.Helper()
	vis := m.visible()
	for i, it := range vis {
		if it.text == token {
			l := m.top()
			l.cursor = i
			return m.replaceTop(l)
		}
	}
	t.Fatalf("token %q not in visible set %v", token, tokensOf(vis))
	return m
}

func tokensOf(items []item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.text
	}
	return out
}

func enter(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return mm, cmd
}

func TestScopedOpensCommandOptions(t *testing.T) {
	m := NewScoped(fakeSuggest, "/model")
	vis := m.visible()
	// First row is the "run as-is" entry; the model values follow.
	if len(vis) == 0 || !vis[0].bare {
		t.Fatalf("first row should be the bare run entry, got %v", tokensOf(vis))
	}
	if got := tokensOf(vis[1:]); len(got) != 2 || got[0] != "gpt-4o" {
		t.Fatalf("scoped /model values = %v, want [gpt-4o claude-opus]", got)
	}
}

// TestEveryScopedCommandPreservesBareAction validates the whole command
// surface at once: opening ANY command scoped must lead with a "run as-is"
// entry that composes to exactly the bare command, so no command can lose the
// behavior it had when typed with no arguments.
func TestEveryScopedCommandPreservesBareAction(t *testing.T) {
	stub := func(string) []Suggestion { return sg("alpha", "beta") } // always has options
	for _, rc := range RootCommands() {
		m := NewScoped(stub, rc.Name)
		items := m.top().items
		if len(items) == 0 || !items[0].bare {
			t.Errorf("%s: scoped level missing the bare run entry", rc.Name)
			continue
		}
		// The bare entry describes what running the command with no arguments
		// does (its summary), not just a generic "run as-is" — so commands like
		// /config that show an overview don't look empty.
		if d := items[0].desc; !strings.Contains(d, rc.Summary()) {
			t.Errorf("%s: bare entry desc %q omits the command summary %q", rc.Name, d, rc.Summary())
		}
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // cursor at 0 = bare
		mm := next.(Model)
		if mm.result != rc.Name {
			t.Errorf("%s: bare entry composed %q, want %q", rc.Name, mm.result, rc.Name)
		}
		if cmd == nil {
			t.Errorf("%s: bare entry did not finish", rc.Name)
		}
	}
}

func TestNestedLevelAlsoHasBareEntry(t *testing.T) {
	m := NewScoped(fakeSuggest, "/config")
	m = moveTo(t, m, "ui")
	m, _ = enter(t, m) // drill to "/config ui"
	if len(m.top().items) == 0 || !m.top().items[0].bare {
		t.Fatal("nested /config ui level missing bare run entry")
	}
	m, cmd := enter(t, m) // cursor at 0 = bare → run "/config ui" as-is
	assertResult(t, m, cmd, "/config ui")
}

// TestBareEntryInheritsSubcommandDescription validates the gap fix: drilling
// into a described subcommand carries THAT subcommand's description down to the
// level's "run as-is" entry, instead of repeating the parent command's summary.
// e.g. "↵ /config security" describes security, not /config. This works for any
// subcommand the completer describes.
func TestBareEntryInheritsSubcommandDescription(t *testing.T) {
	m := NewScoped(fakeSuggest, "/config")

	var uiDesc string
	for _, it := range m.top().items {
		if it.text == "ui" {
			uiDesc = it.desc
		}
	}
	if uiDesc == "" {
		t.Fatal("the 'ui' subcommand should carry a description from the suggester")
	}

	m = moveTo(t, m, "ui")
	m, _ = enter(t, m) // drill into /config ui
	bare := m.top().items[0]
	if !bare.bare {
		t.Fatal("first item at the drilled level should be the run-as-is entry")
	}
	if bare.desc != uiDesc {
		t.Errorf("run-as-is desc = %q, want the subcommand description %q", bare.desc, uiDesc)
	}
}

func TestBareEntryRunsCommandAsIs(t *testing.T) {
	// Selecting the "run as-is" entry executes the command with no arguments,
	// preserving the bare-command behavior (e.g. /config overview, /switch
	// provider picker).
	m := NewScoped(fakeSuggest, "/config")
	if !m.top().items[0].bare {
		t.Fatal("scoped /config should lead with the bare run entry")
	}
	// Cursor starts at 0 (the bare entry).
	m, cmd := enter(t, m)
	assertResult(t, m, cmd, "/config")
}

func TestScopedDrillDownToLeafComposesLine(t *testing.T) {
	m := NewScoped(fakeSuggest, "/config")
	m = moveTo(t, m, "ui")
	m, _ = enter(t, m) // drill: /config ui
	if m.depth() != 1 {
		t.Fatalf("depth = %d, want 1", m.depth())
	}
	m = moveTo(t, m, "theme")
	m, _ = enter(t, m) // drill: /config ui theme
	m = moveTo(t, m, "dark")
	m, cmd := enter(t, m) // leaf
	assertResult(t, m, cmd, "/config ui theme dark")
}

func TestScopedDynamicValueFinishes(t *testing.T) {
	m := NewScoped(fakeSuggest, "/model")
	m = moveTo(t, m, "claude-opus")
	m, cmd := enter(t, m)
	assertResult(t, m, cmd, "/model claude-opus")
}

// TestStatelessCompleterDoesNotLoop guards the original bug: a stateless value
// completer (/model re-offers the model list after a pick) must finish on the
// first selection instead of drilling into an identical list forever.
func TestStatelessCompleterDoesNotLoop(t *testing.T) {
	m := NewScoped(fakeSuggest, "/model")
	m = moveTo(t, m, "gpt-4o")
	m, cmd := enter(t, m)
	if m.depth() != 0 {
		t.Fatalf("selecting a value drilled to depth %d instead of finishing", m.depth())
	}
	assertResult(t, m, cmd, "/model gpt-4o")
}

func TestScopedFlagThenValue(t *testing.T) {
	m := NewScoped(fakeSuggest, "/switch")
	m = moveTo(t, m, "--model")
	m, _ = enter(t, m) // drill into flag's values
	m = moveTo(t, m, "gpt-4o")
	m, cmd := enter(t, m)
	assertResult(t, m, cmd, "/switch --model gpt-4o")
}

func TestRootDrillsViaSuggest(t *testing.T) {
	m := NewRoot(fakeSuggest)
	m = moveTo(t, m, "/config")
	m, _ = enter(t, m) // drill into /config options
	if m.depth() != 1 {
		t.Fatalf("root drill depth = %d, want 1", m.depth())
	}
	// bare run entry + the three sections.
	if got := tokensOf(m.visible()); len(got) != 4 {
		t.Fatalf("/config options = %v, want 4 (bare + 3)", got)
	}
}

func TestFreeArgHintPrefillsTrailingSpace(t *testing.T) {
	// A command whose only next-token is a <hint> finishes with a trailing
	// space so the user types the value.
	m := NewRoot(fakeSuggest)
	m = moveTo(t, m, "/lsp")
	m, cmd := enter(t, m)
	assertResult(t, m, cmd, "/lsp ")
}

func TestLeafCommandWithoutOptionsSubmits(t *testing.T) {
	m := NewRoot(fakeSuggest)
	m = moveTo(t, m, "/help") // fakeSuggest returns nil → no options, no hint
	m, cmd := enter(t, m)
	assertResult(t, m, cmd, "/help")
}

func TestTabPrefillsWithoutDrilling(t *testing.T) {
	m := NewRoot(fakeSuggest)
	m = moveTo(t, m, "/config")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm := next.(Model)
	assertResult(t, mm, cmd, "/config ")
}

func TestFilterNarrowsVisible(t *testing.T) {
	m := NewScoped(fakeSuggest, "/switch")
	m = typeRunes(t, m, "max")
	vis := m.visible()
	if len(vis) != 1 || vis[0].text != "--max-tokens" {
		t.Fatalf("filter 'max' = %v, want [--max-tokens]", tokensOf(vis))
	}
}

func TestEscClearsQueryThenCancels(t *testing.T) {
	m := NewScoped(fakeSuggest, "/switch")
	m = typeRunes(t, m, "mod")
	m = key(t, m, tea.KeyMsg{Type: tea.KeyEsc}) // clears query
	if m.top().query != "" {
		t.Fatalf("query not cleared: %q", m.top().query)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // cancels
	mm := next.(Model)
	if mm.result != "" {
		t.Errorf("cancel result = %q, want empty", mm.result)
	}
	if cmd == nil {
		t.Error("cancel emitted no quit command")
	}
}

func TestBackspaceAtDepthPops(t *testing.T) {
	m := NewScoped(fakeSuggest, "/config")
	m = moveTo(t, m, "ui")
	m, _ = enter(t, m)
	if m.depth() != 1 {
		t.Fatalf("depth = %d, want 1", m.depth())
	}
	m = key(t, m, tea.KeyMsg{Type: tea.KeyBackspace}) // empty query → pop
	if m.depth() != 0 {
		t.Errorf("backspace did not pop: depth %d", m.depth())
	}
}

func TestNavigationClamps(t *testing.T) {
	m := NewScoped(fakeSuggest, "/config")
	m = key(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.top().cursor != 0 {
		t.Errorf("cursor above 0: %d", m.top().cursor)
	}
	for i := 0; i < 10; i++ {
		m = key(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.top().cursor != len(m.visible())-1 {
		t.Errorf("cursor = %d, want last %d", m.top().cursor, len(m.visible())-1)
	}
}

func TestCleanItemsDropsHintsAndEchoes(t *testing.T) {
	raw := []Suggestion{
		{Text: "ui"}, {Text: "<path>"}, {Text: "/config"}, {Text: "general"},
	}
	got := tokensOf(cleanItems(raw))
	want := []string{"ui", "general"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("cleanItems = %v, want %v", got, want)
	}
	if !rawHasHint(raw) {
		t.Error("rawHasHint should detect <path>")
	}
}

func TestComposeLine(t *testing.T) {
	cases := []struct{ prefix, token, want string }{
		{"", "/help", "/help"},
		{"/config", "ui", "/config ui"},
		{"/config ui", "", "/config ui"},
	}
	for _, c := range cases {
		if got := composeLine(c.prefix, c.token); got != c.want {
			t.Errorf("composeLine(%q,%q) = %q, want %q", c.prefix, c.token, got, c.want)
		}
	}
}

// TestRowRoleColorsAreDistinct guards the original UI bug: the row label and
// its description must resolve to different theme colors (previously both
// landed on Muted), under every registered theme.
func TestRowRoleColorsAreDistinct(t *testing.T) {
	for _, name := range theme.Names() {
		if err := theme.SetActive(name); err != nil {
			t.Fatalf("SetActive(%q): %v", name, err)
		}
		th := theme.Active()
		label := th.ColorFor(theme.RoleModelName).Hex   // token
		descU := th.ColorFor(theme.RoleMuted).Hex       // unselected description
		descS := th.ColorFor(theme.RoleExplanation).Hex // selected description
		marker := th.ColorFor(theme.RoleReasoning).Hex  // selected marker
		if label == descU {
			t.Errorf("theme %q: label and unselected description share %s", name, label)
		}
		if label == descS {
			t.Errorf("theme %q: label and selected description share %s", name, label)
		}
		if label == marker {
			t.Errorf("theme %q: label and selected marker share %s", name, label)
		}
	}
}

// TestDescTruncatedToWidth guards against a long description (e.g. /config's
// summary) wrapping into a giant multi-row line: it is trimmed with an ellipsis
// to the space left on the row, and dropped entirely when the row is too narrow.
func TestDescTruncatedToWidth(t *testing.T) {
	m := NewScoped(fakeSuggest, "/config")
	m.width = 40
	long := strings.Repeat("x", 200)

	got := m.fitDesc(long, 10)
	if got == "" {
		t.Fatal("expected a truncated description, got empty")
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated desc should end with an ellipsis, got %q", got)
	}
	if w := 2 + 10 + 2 + lipgloss.Width(got); w > m.width {
		t.Errorf("row width %d exceeds terminal width %d", w, m.width)
	}
	// A short description is left untouched.
	if m.fitDesc("short", 10) != "short" {
		t.Error("short description should not be truncated")
	}
	// Too narrow → description dropped rather than mangled.
	m.width = 12
	if m.fitDesc(long, 10) != "" {
		t.Error("expected the description to be dropped on a too-narrow row")
	}
}

func TestViewRenders(t *testing.T) {
	theme.SetProfile(theme.ProfileANSI)
	m := NewRoot(fakeSuggest)
	m.width, m.height = 100, 30
	if m.View() == "" {
		t.Error("root View empty")
	}
	m = moveTo(t, m, "/config")
	m, _ = enter(t, m)
	if m.View() == "" {
		t.Error("drilled View empty")
	}
	m.quitting = true
	if m.View() != "" {
		t.Error("quitting View should be empty")
	}
}

func assertResult(t *testing.T, m Model, cmd tea.Cmd, want string) {
	t.Helper()
	if m.result != want {
		t.Errorf("result = %q, want %q", m.result, want)
	}
	if cmd == nil {
		t.Error("expected quit command, got nil")
	}
	if !m.quitting {
		t.Error("model not quitting")
	}
}
