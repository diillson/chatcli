/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/coder"
)

func TestStdinDraftParkRestore(t *testing.T) {
	var line strings.Builder
	var d stdinDraft
	line.WriteString("refactor the m")

	if got := d.park(&line); got != "refactor the m" || line.Len() != 0 {
		t.Fatalf("park: returned %q, buffer %q", got, line.String())
	}
	// A question raised while another is open shares the parked draft.
	if got := d.park(&line); got != "refactor the m" {
		t.Fatalf("nested park returned %q", got)
	}
	d.restore(&line)
	if line.Len() != 0 {
		t.Fatalf("draft came back while a question is still open: %q", line.String())
	}
	if got := d.pending(&line); got != "refactor the m" {
		t.Fatalf("a reader shutting down would hand over %q", got)
	}
	// Typed between the answer and the restore: kept, after the draft.
	line.WriteString("X")
	d.restore(&line)
	if line.String() != "refactor the mX" || d.parked != "" || d.depth != 0 {
		t.Fatalf("restore: buffer %q parked %q depth %d", line.String(), d.parked, d.depth)
	}
	// An unbalanced restore changes nothing.
	d.restore(&line)
	if line.String() != "refactor the mX" {
		t.Fatalf("unbalanced restore: %q", line.String())
	}
}

func TestRequestStdinDraftWithoutReader(t *testing.T) {
	if _, ok := requestStdinDraft(nil, nil, true); ok {
		t.Fatal("no reader, yet the park was acknowledged")
	}
	// A reader that never answers (blocked handing a line over) must not
	// hold the prompt: the request gives up and the prompt goes ahead.
	start := time.Now()
	if _, ok := requestStdinDraft(make(chan stdinDraftReq), make(chan struct{}), true); ok {
		t.Fatal("unanswered park was acknowledged")
	}
	if took := time.Since(start); took > 5*stdinDraftWait {
		t.Fatalf("an unresponsive reader held the prompt for %v", took)
	}
	// A reader that takes the request and then stops answers nobody.
	ctl, done := make(chan stdinDraftReq), make(chan struct{})
	go func() {
		<-ctl
		close(done)
	}()
	if _, ok := requestStdinDraft(ctl, done, true); ok {
		t.Fatal("a reader that stopped mid-request acknowledged the park")
	}
	a := &AgentMode{}
	a.parkTypeaheadDraft()() // no reader: both halves are no-ops
}

// stdinPipe replaces os.Stdin with a pipe for the test and returns its
// write end.
func stdinPipe(t *testing.T) *os.File {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the reader polls a console handle on Windows; a pipe is not one")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = w.Close()
		_ = r.Close()
	})
	return w
}

func awaitPreview(t *testing.T, a *AgentMode, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for a.typeaheadPreviewSnapshot() != want {
		if time.Now().After(deadline) {
			t.Fatalf("input line is %q, want %q", a.typeaheadPreviewSnapshot(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitLine(t *testing.T, a *AgentMode) string {
	t.Helper()
	select {
	case line := <-a.stdinLines:
		return line
	case <-time.After(3 * time.Second):
		t.Fatal("the reader delivered no line")
		return ""
	}
}

// The reported bug, against the real reader: the user is typing while the
// model works, a permission prompt appears, and what they had typed is
// read as part of the answer.
func TestPermissionPromptDoesNotSwallowTheDraft(t *testing.T) {
	w := stdinPipe(t)
	a := &AgentMode{}
	a.startStdinReader(context.Background())
	defer a.stopStdinReader()

	write := func(s string) {
		t.Helper()
		if _, err := w.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}

	// Control — the bug: with nothing setting the draft aside, the answer
	// arrives glued to it, and no prompt accepts that.
	write("refactor the m")
	awaitPreview(t, a, "refactor the m")
	write("y\n")
	if got := awaitLine(t, a); got != "refactor the my" {
		t.Fatalf("control: the reader delivered %q", got)
	}

	// The fix, through the hook the security prompts call.
	write("refactor the m")
	awaitPreview(t, a, "refactor the m")
	var restore func()
	out := captureStdout(t, func() { restore = a.parkTypeaheadDraft() })
	if !strings.Contains(out, "refactor the m") {
		t.Fatalf("the user was not told the draft was set aside:\n%s", out)
	}
	awaitPreview(t, a, "")
	write("y\n")
	if got := awaitLine(t, a); got != "y" {
		t.Fatalf("the prompt would read %q as the answer", got)
	}
	restore()
	awaitPreview(t, a, "refactor the m")
	// The user carries on from where they were.
	write("ethod\n")
	if got := awaitLine(t, a); got != "refactor the method" {
		t.Fatalf("the draft came back as %q", got)
	}
}

// A confirmation read through readLine gets the same protection, and a
// question read line by line announces the parked draft once.
func TestReadLineSetsTheDraftAside(t *testing.T) {
	w := stdinPipe(t)
	a := &AgentMode{}
	a.startStdinReader(context.Background())
	defer a.stopStdinReader()

	_, _ = w.WriteString("half an instr")
	awaitPreview(t, a, "half an instr")
	answers := make(chan string, 2)
	out := captureStdout(t, func() {
		go func() {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.WriteString("s\n")
		}()
		answers <- a.readLineSignal(nil)
		awaitPreview(t, a, "half an instr")
		go func() {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.WriteString("n\n")
		}()
		answers <- a.readLineSignal(nil)
	})
	if first, second := <-answers, <-answers; first != "s" || second != "n" {
		t.Fatalf("answers read as %q and %q", first, second)
	}
	awaitPreview(t, a, "half an instr")
	if n := strings.Count(out, "half an instr"); n != 1 {
		t.Fatalf("the parked draft was announced %d times:\n%s", n, out)
	}
}

// A reader replaced while a question is open hands the parked draft to its
// successor instead of dropping it.
func TestParkedDraftSurvivesAReaderSwap(t *testing.T) {
	w := stdinPipe(t)
	a := &AgentMode{}
	a.startStdinReader(context.Background())
	defer a.stopStdinReader()

	_, _ = w.WriteString("keep me")
	awaitPreview(t, a, "keep me")
	var restore func()
	_ = captureStdout(t, func() { restore = a.parkTypeaheadDraft() })
	a.suspendStdinReader()
	a.resumeStdinReader(context.Background())
	restore() // addressed to the reader that is gone: a no-op
	awaitPreview(t, a, "keep me")
}

// The hook is installed for as long as the loop owns stdin.
func TestDraftParkerFollowsTheReaderLifecycle(t *testing.T) {
	stdinPipe(t)
	a := &AgentMode{}
	called := 0
	coder.SetSecurityPromptDraftParker(func() func() { called++; return nil })
	a.startStdinReader(context.Background())
	a.stopStdinReader()
	// After the last stop the loop's hook is gone (and so is the one this
	// test planted before the start, which the start replaced).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = captureStdout(t, func() { _ = coder.PromptSecurityCheckGuarded(ctx, "@read", "{}", make(chan string)) })
	if called != 0 {
		t.Fatal("a draft hook outlived the reader it belongs to")
	}
}
