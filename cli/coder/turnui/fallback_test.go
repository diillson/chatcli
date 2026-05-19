/*
 * ChatCLI - Coder turn-UI fallback tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestShouldActivate_Truthy enumerates the environment shapes that
// are expected to enable the split UI. Each "true" answer here is a
// promise to the user: when these conditions hold, /coder uses the
// split layout. Regressing any of them flags as a test failure.
func TestShouldActivate_Truthy(t *testing.T) {
	env := Environment{
		StdinFD:     0,
		IsStdinTTY:  true,
		IsStdoutTTY: true,
		Rows:        40,
		Cols:        120,
		GOOS:        "linux",
		TermType:    "xterm-256color",
	}
	assert.True(t, ShouldActivate(env))
}

// TestShouldActivate_Veto walks every fallback branch and confirms
// it returns false. Each subtest names the exact "no" reason so a
// future bug report ("/coder isn't using the split UI") can be
// quickly mapped to which signal pushed the user into fallback.
func TestShouldActivate_Veto(t *testing.T) {
	base := Environment{
		StdinFD:     0,
		IsStdinTTY:  true,
		IsStdoutTTY: true,
		Rows:        40,
		Cols:        120,
		GOOS:        "linux",
		TermType:    "xterm-256color",
	}

	tests := []struct {
		name string
		mut  func(*Environment)
	}{
		{"CHATCLI_TURNUI=off pinned", func(e *Environment) { e.ForceDisabled = true }},
		{"stdin is a pipe", func(e *Environment) { e.IsStdinTTY = false }},
		{"stdout is redirected", func(e *Environment) { e.IsStdoutTTY = false }},
		{"TERM=dumb (Emacs M-x shell)", func(e *Environment) { e.TermType = "dumb" }},
		{"empty TERM (some CI runners)", func(e *Environment) { e.TermType = "" }},
		{"terminal too short", func(e *Environment) { e.Rows = MinRowsRequired - 1 }},
		{"terminal too narrow", func(e *Environment) { e.Cols = MinColsRequired - 1 }},
		{"zero size", func(e *Environment) { e.Rows = 0; e.Cols = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := base
			tc.mut(&env)
			assert.False(t, ShouldActivate(env), "expected fallback for: %s", tc.name)
		})
	}
}

// TestShouldActivate_ForceDisabledBeatsEverything makes the escape
// hatch explicit. A user who sets CHATCLI_TURNUI=off has decided the
// split UI is broken on their setup; we honor that no matter how good
// every other signal looks.
func TestShouldActivate_ForceDisabledBeatsEverything(t *testing.T) {
	env := Environment{
		IsStdinTTY:    true,
		IsStdoutTTY:   true,
		Rows:          200,
		Cols:          400,
		GOOS:          "linux",
		TermType:      "xterm-256color",
		ForceDisabled: true,
	}
	assert.False(t, ShouldActivate(env))
}

// TestMinSizes_AreConservative is a tripwire: if a future change tries
// to push the minimum size down for "we can fit a smaller UI" reasons,
// this test fails and forces a code review of the UX consequences.
// The thresholds are deliberately generous because below them the split
// looks cramped and is worse than the legacy fallback.
func TestMinSizes_AreConservative(t *testing.T) {
	assert.GreaterOrEqual(t, MinRowsRequired, 10,
		"too small: status + input + 1 content row would be unusable")
	assert.GreaterOrEqual(t, MinColsRequired, 40,
		"too narrow: the status line would wrap and break the layout")
}
