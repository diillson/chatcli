/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// quietStdout discards what the prompt prints for the duration of a test.
func quietStdout(t *testing.T) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	go func() { _, _ = io.Copy(io.Discard, r) }()
	t.Cleanup(func() {
		os.Stdout = orig
		_ = w.Close()
	})
}

func TestParseSecurityAnswer(t *testing.T) {
	cases := []struct {
		in     string
		isExec bool
		want   SecurityDecision
		ok     bool
	}{
		{"", false, DecisionRunOnce, true},
		{" Y ", false, DecisionRunOnce, true},
		{"sim", true, DecisionRunOnce, true},
		{"n", true, DecisionDenyOnce, true},
		{"NÃO", false, DecisionDenyOnce, true},
		{"a", false, DecisionAllowAlways, true},
		{"deny", false, DecisionDenyForever, true},
		// A command with no rule pattern is asked yes/no only.
		{"a", true, DecisionDenyOnce, false},
		{"refactor the y", false, DecisionDenyOnce, false},
		{"yy", true, DecisionDenyOnce, false},
	}
	for _, c := range cases {
		got, ok := parseSecurityAnswer(c.in, c.isExec)
		if got != c.want || ok != c.ok {
			t.Errorf("parseSecurityAnswer(%q, exec=%v) = %v, %v; want %v, %v", c.in, c.isExec, got, ok, c.want, c.ok)
		}
	}
}

func promptWith(t *testing.T, answers ...string) SecurityDecision {
	t.Helper()
	quietStdout(t)
	ch := make(chan string, len(answers))
	for _, a := range answers {
		ch <- a
	}
	done := make(chan SecurityDecision, 1)
	go func() {
		done <- PromptSecurityCheckWithContext(context.Background(), "@coder", `{"cmd":"write","args":{"file":"a.txt"}}`, nil, ch)
	}()
	select {
	case d := <-done:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt is still waiting for an answer")
		return DecisionCanceled
	}
}

// An answer the prompt cannot read used to deny the action on the spot. It
// is asked again; the action is decided by an answer the user meant.
func TestUnreadableAnswerIsAskedAgain(t *testing.T) {
	if d := promptWith(t, "refactor the y", "y"); d != DecisionRunOnce {
		t.Fatalf("typo then yes = %v, want run once", d)
	}
	if d := promptWith(t, "wat", "huh", "n"); d != DecisionDenyOnce {
		t.Fatalf("typos then no = %v, want deny once", d)
	}
	// It does not ask forever, and never runs on unreadable input.
	if d := promptWith(t, "one", "two", "three", "y"); d != DecisionDenyOnce {
		t.Fatalf("three unreadable answers = %v, want deny once", d)
	}
}

// With nobody able to answer, the end of input is never read as a yes.
func TestClosedInputDenies(t *testing.T) {
	quietStdout(t)
	ch := make(chan string)
	close(ch)
	if d := PromptSecurityCheckWithContext(context.Background(), "@coder", `{"cmd":"exec","args":{"cmd":"ls"}}`, nil, ch); d != DecisionDenyOnce {
		t.Fatalf("closed input = %v, want deny once", d)
	}
}

// The guarded prompts set the user's draft aside before asking and put it
// back after the decision, on every way out.
func TestGuardedPromptParksAndRestoresTheDraft(t *testing.T) {
	quietStdout(t)
	var events []string
	SetSecurityPromptDraftParker(func() func() {
		events = append(events, "park")
		return func() { events = append(events, "restore") }
	})
	t.Cleanup(func() { SetSecurityPromptDraftParker(nil) })

	ch := make(chan string, 1)
	ch <- "stale line typed before the prompt"
	go func() {
		time.Sleep(150 * time.Millisecond)
		ch <- "y"
	}()
	if d := PromptSecurityCheckGuarded(context.Background(), "@read", `{"file":"a.txt"}`, ch); d != DecisionRunOnce {
		t.Fatalf("decision = %v", d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d := PromptSecurityCheckWithContextGuarded(ctx, "@read", `{"file":"a.txt"}`, nil, make(chan string)); d != DecisionCanceled {
		t.Fatalf("cancelled prompt = %v", d)
	}
	want := []string{"park", "restore", "park", "restore"}
	if len(events) != len(want) {
		t.Fatalf("hook calls = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("hook calls = %v, want %v", events, want)
		}
	}
}
