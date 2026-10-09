/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func withStdinInteractive(t *testing.T, v bool) {
	t.Helper()
	prev := stdinIsInteractive
	stdinIsInteractive = func() bool { return v }
	t.Cleanup(func() { stdinIsInteractive = prev })
}

// A one-shot coder without a terminal used to hang on the security prompt
// until the run's timeout (stdin at EOF never answers). It must deny at once,
// tell the model the truth (not a user denial) and close the tool call.
func TestNoTerminalAskBlocked_DeniesAtOnce(t *testing.T) {
	withStdinInteractive(t, false)
	rec := &sinkRecorder{}
	c := &ChatCLI{}
	a := NewAgentMode(c, zap.NewNop())
	a.events = rec
	var rendered []string
	blocked := a.noTerminalAskBlocked("@coder", `{"cmd":"patch","args":{"file":"sum.go"}}`, func(s string) { rendered = append(rendered, s) })
	if !blocked {
		t.Fatal("ask without a terminal must be blocked")
	}
	if len(rendered) != 1 || rendered[0] == "" {
		t.Fatalf("the user must see why: %v", rendered)
	}
	if len(c.history) != 1 || !strings.Contains(c.history[0].Content, "no interactive terminal") ||
		strings.Contains(c.history[0].Content, "DENIED BY USER") {
		t.Fatalf("model feedback must say there was no terminal, not a user denial: %+v", c.history)
	}
	if len(rec.ends) != 1 || !rec.ends[0].IsError {
		t.Fatalf("blocked tool call must be closed as an error: %+v", rec.ends)
	}
}

func TestNoTerminalAskBlocked_TerminalPrompts(t *testing.T) {
	withStdinInteractive(t, true)
	c := &ChatCLI{}
	a := NewAgentMode(c, zap.NewNop())
	if a.noTerminalAskBlocked("@coder", `{"cmd":"exec"}`, func(string) { t.Error("must not render") }) {
		t.Fatal("with a terminal the prompt path stays in charge")
	}
	if len(c.history) != 0 {
		t.Fatal("no feedback when nothing was blocked")
	}
}

func TestWorkerNoTerminalDenial(t *testing.T) {
	withStdinInteractive(t, false)
	if denied, msg := workerNoTerminalDenial("@coder"); !denied || msg == "" {
		t.Fatal("worker ask without a terminal must be denied with a reason")
	}
	withStdinInteractive(t, true)
	if denied, _ := workerNoTerminalDenial("@coder"); denied {
		t.Fatal("worker ask with a terminal keeps prompting")
	}
}
