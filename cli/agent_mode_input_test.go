package cli

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/paste"
)

// Tests for the helpers extracted from readLineWithEditing. The
// go-prompt path needs a real TTY and isn't unit-testable, but the
// non-TTY fallback, paste-replay, multiline-trigger detection and
// continuation-line accumulator are pure functions over their
// inputs and pin the contract that drove the rewrite.

func TestReadLinePlainFromReaderTrimsAndStrips(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"hello\n", "hello"},
		{"  spaced  \n", "spaced"},
		{"crlf\r\n", "crlf"},
		{"", ""}, // EOF before any byte → empty line
	}
	for _, c := range cases {
		got := readLinePlainFromReader(bufio.NewReader(strings.NewReader(c.in)))
		if got != c.want {
			t.Errorf("input %q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestApplyPendingPasteInfoNoPasteIsNoOp(t *testing.T) {
	a := &AgentMode{cli: &ChatCLI{}}
	if got := a.applyPendingPasteInfo("hello"); got != "hello" {
		t.Errorf("expected pass-through when no paste pending, got %q", got)
	}
}

func TestApplyPendingPasteInfoSwapsPlaceholder(t *testing.T) {
	cli := &ChatCLI{
		lastPasteInfo: &paste.Info{
			Placeholder: "<<PASTE>>",
			Content:     "actual pasted content",
		},
	}
	a := &AgentMode{cli: cli}
	got := a.applyPendingPasteInfo("user typed <<PASTE>> here")
	if got != "user typed actual pasted content here" {
		t.Errorf("placeholder not replaced: %q", got)
	}
	if cli.lastPasteInfo != nil {
		t.Error("lastPasteInfo must be cleared so next chat-mode prompt doesn't see stale state")
	}
}

func TestApplyPendingPasteInfoClearsEvenWhenPlaceholderAbsent(t *testing.T) {
	// Pinned because the chat-mode REPL relies on lastPasteInfo
	// being drained at every prompt — leaking it across a coder
	// iteration would surface a stale "paste detected" banner.
	cli := &ChatCLI{
		lastPasteInfo: &paste.Info{
			Placeholder: "<<PASTE>>",
			Content:     "actual pasted content",
		},
	}
	a := &AgentMode{cli: cli}
	got := a.applyPendingPasteInfo("no placeholder here")
	if got != "no placeholder here" {
		t.Errorf("line should be untouched when placeholder absent: %q", got)
	}
	if cli.lastPasteInfo != nil {
		t.Error("lastPasteInfo must still be cleared even when placeholder did not match")
	}
}

func TestApplyPendingPasteInfoSurvivesNilCLI(t *testing.T) {
	// Defensive: if AgentMode is constructed without a parent (only
	// happens in narrow test paths today), don't panic.
	a := &AgentMode{}
	if got := a.applyPendingPasteInfo("x"); got != "x" {
		t.Errorf("expected pass-through when cli is nil, got %q", got)
	}
}

func TestIsMultilineTrigger(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"---", true},
		{"```", true},
		{"——", false}, // em-dashes, not three hyphens
		{"--", false},
		{"```go", false}, // language-tagged fence is NOT a multiline trigger
		{"", false},
		{"hello", false},
	}
	for _, c := range cases {
		if got := isMultilineTrigger(c.in); got != c.want {
			t.Errorf("isMultilineTrigger(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRunMultilineSessionAccumulatesUntilDelimiter(t *testing.T) {
	a := &AgentMode{}
	// Feed three content lines and the closing delimiter through
	// our own bufio.Reader so the test is hermetic — no os.Stdin.
	input := "first line\nsecond line\nthird line\n---\n"
	reader := bufio.NewReader(strings.NewReader(input))

	got, err := a.runMultilineSession("---", reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"first line", "second line", "third line"} {
		if !strings.Contains(got, want) {
			t.Errorf("multiline output missing %q: %q", want, got)
		}
	}
}

func TestReadLineWithEditingFallsBackOnNonTTYStdin(t *testing.T) {
	// In CI / test runs stdin is a pipe, not a TTY — so this test
	// exercises the fallback branch that was the whole reason we
	// kept the bufio path around. Redirect stdin to a fresh pipe to
	// guarantee non-TTY regardless of how `go test` was launched.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = orig
		_ = r.Close()
	}()

	go func() {
		_, _ = w.WriteString("piped input\n")
		_ = w.Close()
	}()

	a := &AgentMode{cli: &ChatCLI{}}
	got, err := a.readLineWithEditing()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "piped input" {
		t.Errorf("got %q, want %q", got, "piped input")
	}
}

func TestRunMultilineSessionWithBacktickFence(t *testing.T) {
	// The "```" trigger must round-trip the same way as "---".
	// Pinned so the parallel fence support in isMultilineTrigger
	// stays wired to the accumulator.
	a := &AgentMode{}
	input := "fenced content\n```\n"
	reader := bufio.NewReader(strings.NewReader(input))

	got, err := a.runMultilineSession("```", reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "fenced content") {
		t.Errorf("backtick fence didn't capture content: %q", got)
	}
}
