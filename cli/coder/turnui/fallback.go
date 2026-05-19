/*
 * ChatCLI - Coder turn-UI fallback detection
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package turnui implements the split-pane terminal UI used by the
// coder/agent loop: the agent's output scrolls in the upper region,
// a status row carries the live spinner, and an always-typable input
// row sits at the bottom. The UI is built on three primitives —
// DECSTBM scroll region for the content/status split, TTY raw mode for
// keystroke ownership, and a mini line-editor for the input row.
//
// Activation is conservative: anything that could leave the user with
// a wedged terminal (non-TTY stdin, undersized terminal, the legacy
// Windows console without ANSI support) routes through ShouldActivate
// returning false, and the agent loop falls back to its previous
// single-line spinner. The package never panics on a hostile terminal —
// failure modes are always "don't activate", never "activate broken".
package turnui

import (
	"os"
	"runtime"

	"golang.org/x/term"
)

// MinRowsRequired is the smallest terminal height that can host the
// split UI: 1 for status + 1 for input + a working content area. We
// pick 10 (not 3) so the user actually has room to see scrolling
// output; below this the legacy renderer is friendlier. The check is
// re-evaluated on SIGWINCH so a user who resizes back up recovers.
const MinRowsRequired = 10

// MinColsRequired keeps the status line from wrapping its model name
// + duration + queue indicator. 40 cols fits "[gpt-4] [12s] (3 queued)"
// with the spinner and a few dots; thinner terminals look mangled.
const MinColsRequired = 40

// Environment captures the inputs ShouldActivate evaluates. Carrying
// them as a struct lets tests stub the entire environment without
// touching globals, and makes the rule set explicit for code review:
// every "no" branch in ShouldActivate maps to one Environment field.
type Environment struct {
	// StdinFD is the file descriptor of stdin. -1 means "not a TTY"
	// (piped input, CI), which forces fallback regardless of size.
	StdinFD int

	// IsStdinTTY mirrors term.IsTerminal(StdinFD). We carry it
	// separately so tests can simulate a piped stdin without
	// changing os.Stdin.
	IsStdinTTY bool

	// IsStdoutTTY is required because the status row and input row
	// are both written to stdout — if stdout is redirected (the
	// user is piping the agent's output into a file), drawing the
	// UI would mangle the file.
	IsStdoutTTY bool

	// Rows / Cols are the terminal dimensions in cells, normally
	// read from term.GetSize. Zero means "unknown" and is treated
	// as too small.
	Rows int
	Cols int

	// GOOS allows the Windows-specific fallback to be tested on
	// any platform.
	GOOS string

	// TermType is the value of $TERM. The dumb terminal ("dumb"
	// from Emacs M-x shell, "unknown" from some CI runners) does
	// not honor cursor positioning sequences, so we fall back.
	TermType string

	// NoColor mirrors the NO_COLOR env var. A user with NO_COLOR
	// set expects plain output; the split UI prints color codes
	// even when off, so we honor that signal as a fallback hint.
	NoColor bool

	// ForceDisabled lets the user pin the legacy renderer via the
	// CHATCLI_TURNUI=off escape hatch, regardless of every other
	// signal. The opposite ("force on") is intentionally NOT
	// supported — if the environment says "no", we trust it.
	ForceDisabled bool
}

// DetectEnvironment snapshots the live process state into an
// Environment. The snapshot is intentional: ShouldActivate is meant
// to be cheap and deterministic per call site, so callers freeze the
// state once and rely on SIGWINCH-driven re-evaluation for resize.
func DetectEnvironment() Environment {
	env := Environment{
		StdinFD:       int(os.Stdin.Fd()), // #nosec G115 -- Fd() is uintptr; values fit int on every supported platform.
		IsStdinTTY:    term.IsTerminal(int(os.Stdin.Fd())),
		IsStdoutTTY:   term.IsTerminal(int(os.Stdout.Fd())),
		GOOS:          runtime.GOOS,
		TermType:      os.Getenv("TERM"),
		NoColor:       os.Getenv("NO_COLOR") != "",
		ForceDisabled: os.Getenv("CHATCLI_TURNUI") == "off",
	}
	if env.IsStdoutTTY {
		if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
			env.Cols = w
			env.Rows = h
		}
	}
	return env
}

// ShouldActivate returns true when every prerequisite for the split UI
// is satisfied and the legacy fallback should be skipped. The function
// is pure: same Environment in, same answer out — so the agent loop
// can call it once at Begin and trust the result for the turn (modulo
// SIGWINCH-triggered re-evaluation).
//
// The order of checks mirrors blast radius. ForceDisabled is first
// because it's the user's explicit veto; non-TTY is second because
// drawing escape codes into a non-terminal corrupts logs; dumb-TERM is
// third because it's the loudest "I don't speak ANSI" signal; size is
// last because it's the most likely transient failure (a small split
// pane that the user might resize).
func ShouldActivate(env Environment) bool {
	if env.ForceDisabled {
		return false
	}
	if !env.IsStdinTTY || !env.IsStdoutTTY {
		return false
	}
	if env.TermType == "dumb" || env.TermType == "" {
		return false
	}
	if env.GOOS == "windows" && !windowsAnsiAvailable() {
		return false
	}
	if env.Rows < MinRowsRequired || env.Cols < MinColsRequired {
		return false
	}
	return true
}

// windowsAnsiAvailable is a stub on non-Windows; the real check lives
// in fallback_windows.go and probes Console virtual-terminal mode.
// The split UI only activates on Windows 10+ consoles where ANSI is
// enabled — the legacy cmd.exe / older PowerShell hosts fall through.
func windowsAnsiAvailable() bool {
	if runtime.GOOS != "windows" {
		return true
	}
	return windowsAnsiAvailableImpl()
}
