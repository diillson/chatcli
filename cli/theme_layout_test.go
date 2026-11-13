/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/ui/theme"
)

var layoutANSI = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func plainText(s string) string { return layoutANSI.ReplaceAllString(s, "") }

// Only the themes built for it lay panels out flat; every other theme keeps
// the frame it has always had.
func TestOnlyOnyxLaysPanelsOutFlat(t *testing.T) {
	for _, name := range theme.Names() {
		useTheme(t, name)
		flat := theme.Active().Layout == theme.LayoutFlat
		if want := strings.HasPrefix(name, "onyx"); flat != want {
			t.Errorf("%s: flat layout = %v, want %v", name, flat, want)
		}
	}
}

func TestPanelHelpersFollowTheLayout(t *testing.T) {
	useTheme(t, "dark")
	if got := plainText(uiBox("⚡", "HOOKS", ColorPurple)); got != "╭── ⚡ HOOKS" {
		t.Fatalf("framed title = %q", got)
	}
	if got := plainText(uiPrefix(ColorPurple)); got != "│  " {
		t.Fatalf("framed rail = %q", got)
	}
	if got := plainText(uiBoxEnd(ColorPurple)); !strings.HasPrefix(got, "╰─") {
		t.Fatalf("framed footer = %q", got)
	}

	useTheme(t, "onyx")
	title := plainText(uiBox("⚡", "HOOKS", ColorPurple))
	if !strings.HasPrefix(title, "  HOOKS ─") || strings.ContainsAny(title, "╭│╰⚡") {
		t.Fatalf("flat title = %q", title)
	}
	if got := uiPrefix(ColorPurple); got != "  " {
		t.Fatalf("flat content prefix = %q", got)
	}
	if got := uiBoxEnd(ColorPurple); got != "" {
		t.Fatalf("flat footer = %q", got)
	}
	// A line inside the panel keeps the same budget in both layouts.
	if got := plainText(uiBoxLine(ColorPurple, "content")); got != "  content" {
		t.Fatalf("flat line = %q", got)
	}
}

// The live status line of a turn: under the flat layout it carries the
// session's tokens and cost, which move while the model works.
func TestTurnStatusLine(t *testing.T) {
	ct := NewCostTracker()
	a := &AgentMode{cli: &ChatCLI{costTracker: ct}}

	useTheme(t, "onyx")
	frame, _ := a.buildTurnSpinnerFrame(3*time.Second, "model-x", false)
	line := plainText(frame)
	if !strings.Contains(line, "model-x") || !strings.Contains(line, "3s") {
		t.Fatalf("flat status: %q", line)
	}
	if strings.Contains(line, "tokens") || strings.Contains(line, "$") {
		t.Fatalf("a turn with no usage yet shows usage: %q", line)
	}
	ct.totalInputTokens, ct.totalCompletionTokens, ct.totalCostUSD = 18000, 400, 0.0412
	a.cli.messageQueue = []string{"next"}
	frame, _ = a.buildTurnSpinnerFrame(19*time.Second, "model-x", false)
	line = plainText(frame)
	for _, want := range []string{"model-x", "19s", "18.4K tokens", "$0.04", "1 queued"} {
		if !strings.Contains(line, want) {
			t.Errorf("flat status lacks %q: %q", want, line)
		}
	}
	if strings.ContainsAny(line, "[]|") {
		t.Errorf("flat status kept the bracketed form: %q", line)
	}
	// No tracker wired: the line still renders.
	bare := &AgentMode{cli: &ChatCLI{}}
	if frame, _ = bare.buildTurnSpinnerFrame(time.Second, "model-x", false); !strings.Contains(plainText(frame), "model-x") {
		t.Fatalf("status without a tracker: %q", frame)
	}

	useTheme(t, "dark")
	frame, _ = a.buildTurnSpinnerFrame(19*time.Second, "model-x", false)
	line = plainText(frame)
	if !strings.Contains(line, "[model-x]") || strings.Contains(line, "tokens") {
		t.Fatalf("the framed status line changed: %q", line)
	}
}

func TestPermissionPromptLayout(t *testing.T) {
	prompt := func(tool, args string) string {
		ch := make(chan string, 1)
		ch <- "n"
		return plainText(captureStdout(t, func() {
			_ = coder.PromptSecurityCheckWithContext(context.Background(), tool, args, &coder.SecurityContext{AgentName: "planner", TaskDesc: "ship the release"}, ch)
		}))
	}
	exec := `{"cmd":"exec","args":{"cmd":"make release"}}`

	useTheme(t, "dark")
	framed := prompt("@coder", exec)
	if !strings.Contains(framed, "╭") || !strings.Contains(framed, "make release") {
		t.Fatalf("framed prompt:\n%s", framed)
	}

	useTheme(t, "onyx")
	flat := prompt("@coder", exec)
	if strings.ContainsAny(flat, "╭╮╰╯│") {
		t.Fatalf("the flat prompt still draws a frame:\n%s", flat)
	}
	for _, want := range []string{"make release", "planner", "ship the release"} {
		if !strings.Contains(flat, want) {
			t.Errorf("the flat prompt lacks %q:\n%s", want, flat)
		}
	}
	if n, m := strings.Count(strings.TrimSpace(flat), "\n"), strings.Count(strings.TrimSpace(framed), "\n"); n >= m {
		t.Errorf("the flat prompt is not shorter: %d lines against %d", n, m)
	}
	// A tool with a rule pattern offers all four answers.
	four := prompt("@read", `{"file":"a.txt"}`)
	for _, key := range []string{"y ", "a ", "n ", "d "} {
		if !strings.Contains(four, key) {
			t.Errorf("the flat prompt lacks the %q choice:\n%s", key, four)
		}
	}
}
