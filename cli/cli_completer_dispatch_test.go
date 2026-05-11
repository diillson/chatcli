/*
 * ChatCLI - Tests for the completer dispatch helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The legacy `completer` function used to host a long chain of
 * strings.HasPrefix branches; the refactor turned it into a thin
 * orchestrator over four small helpers. These tests cover each branch
 * separately with strong assertions about what each helper accepts vs.
 * rejects, and what their `matched bool` return means.
 */
package cli

import (
	"reflect"
	"testing"
	"unsafe"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// docWithCursor builds a prompt.Document with a real cursorPosition. The
// go-prompt API keeps the field unexported but the type is part of an
// interactive-input library — there is no security boundary that the test
// is violating. This seam lets us exercise the same Document-shaped helpers
// the real prompt loop would, without spinning up a real terminal.
func docWithCursor(text string, cursor int) prompt.Document {
	d := prompt.Document{Text: text}
	v := reflect.ValueOf(&d).Elem()
	f := v.FieldByName("cursorPosition")
	reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().SetInt(int64(cursor))
	return d
}

func TestDocWithCursor_HonorsCursor(t *testing.T) {
	d := docWithCursor("hello world", 5)
	if got := d.TextBeforeCursor(); got != "hello" {
		t.Fatalf("TextBeforeCursor = %q, want %q", got, "hello")
	}
}

func newCompleterTestCLI(t *testing.T) *ChatCLI {
	t.Helper()
	tmp := t.TempDir()
	mgr := persona.NewManager(zap.NewNop())
	mgr.SetProjectDir(tmp)
	_, _ = mgr.RefreshSkills()
	return &ChatCLI{
		logger:         zap.NewNop(),
		personaHandler: &PersonaHandler{manager: mgr, logger: zap.NewNop()},
		skillHandler:   NewSkillHandler(zap.NewNop(), mgr),
	}
}

func TestRouteSlashPrefix_KnownPrefixesMatch(t *testing.T) {
	cli := newCompleterTestCLI(t)
	cases := []string{
		"/skill list ", "/context list", "/agent",
		"/memory load", "/switch", "/auth login",
		"/plan", "/refine", "/verify", "/reflect",
		"/thinking", "/schedule", "/wait", "/jobs",
		"/parked", "/resume foo", "/cancel-park foo",
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			d := docWithCursor(line, len(line))
			_, matched := cli.routeSlashPrefix(line, d)
			if !matched {
				t.Errorf("expected route to match for %q", line)
			}
		})
	}
}

func TestRouteSlashPrefix_PlainTextDoesNotMatch(t *testing.T) {
	cli := newCompleterTestCLI(t)
	d := docWithCursor("plain text", 10)
	if _, matched := cli.routeSlashPrefix("plain text", d); matched {
		t.Error("plain text should not match any slash route")
	}
}

func TestCompleteAtTokenArgs_FileTriggersPathCompleter(t *testing.T) {
	cli := newCompleterTestCLI(t)
	_, matched := cli.completeAtTokenArgs([]string{"@file"}, "@file ", "")
	if !matched {
		t.Error("@file with empty arg should activate path completion")
	}
}

func TestCompleteAtTokenArgs_CommandTriggersBothCompleters(t *testing.T) {
	cli := newCompleterTestCLI(t)
	out, matched := cli.completeAtTokenArgs([]string{"@command"}, "@command ", "")
	if !matched {
		t.Error("@command should activate completion")
	}
	// Output is a union of systemCommandCompleter + filePathCompleter;
	// we just assert non-nil to confirm the union branch ran.
	if out == nil {
		t.Error("@command should produce a non-nil suggestion slice")
	}
}

func TestCompleteAtTokenArgs_FlagWordSuppresses(t *testing.T) {
	cli := newCompleterTestCLI(t)
	_, matched := cli.completeAtTokenArgs([]string{"@file"}, "@file --foo", "--foo")
	if matched {
		t.Error("a flag-shaped current word should suppress @file completion")
	}
}

func TestCompleteAtTokenArgs_EmptyArgsReturnsFalse(t *testing.T) {
	cli := newCompleterTestCLI(t)
	if _, matched := cli.completeAtTokenArgs(nil, "", ""); matched {
		t.Error("empty args should return matched=false")
	}
}

func TestCompleteBareSlash_SlashAloneReturnsBuiltins(t *testing.T) {
	cli := newCompleterTestCLI(t)
	out, matched := cli.completeBareSlash("/", "/")
	if !matched {
		t.Fatal("'/' alone must trigger the bare-slash completion path")
	}
	if len(out) == 0 {
		t.Fatal("expected built-in command suggestions for '/'")
	}
	// Sanity check: at least a few known built-ins must show up.
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Text] = true
	}
	for _, want := range []string{"/help", "/skill", "/agent"} {
		if !seen[want] {
			t.Errorf("built-in %q missing from bare-slash completion", want)
		}
	}
}

func TestCompleteBareSlash_LineWithSpaceDoesNotMatch(t *testing.T) {
	cli := newCompleterTestCLI(t)
	if _, matched := cli.completeBareSlash("/agent foo", "foo"); matched {
		t.Error("a line that already contains a space must not re-trigger the bare-slash branch")
	}
}

func TestCompleteCommandFlags_UnknownCommandReturnsFalse(t *testing.T) {
	cli := newCompleterTestCLI(t)
	d := docWithCursor("/totally-made-up --x", len("/totally-made-up --x"))
	if _, matched := cli.completeCommandFlags(
		[]string{"/totally-made-up", "--x"},
		"/totally-made-up --x",
		d,
	); matched {
		t.Error("an unknown command must not produce flag suggestions")
	}
}

func TestCompleter_SlashAloneReturnsBuiltinList(t *testing.T) {
	// End-to-end: prove the orchestrator wires routeSlashPrefix +
	// completeBareSlash correctly.
	cli := newCompleterTestCLI(t)
	d := docWithCursor("/", 1)
	out := cli.completer(d)
	if len(out) == 0 {
		t.Fatal("expected the bare-slash branch to produce suggestions")
	}
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Text] = true
	}
	if !seen["/help"] {
		t.Error("built-in /help missing from completer('/') output")
	}
}

func TestCompleter_AtFileShowsPathCompletion(t *testing.T) {
	cli := newCompleterTestCLI(t)
	d := docWithCursor("@file ", 6)
	// Path completer's output depends on cwd; we only verify the
	// dispatcher reached completeAtTokenArgs by checking a slice came back.
	out := cli.completer(d)
	if out == nil {
		t.Error("@file dispatch should return a non-nil slice (even if empty)")
	}
}
