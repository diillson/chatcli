//go:build windows

/*
 * ChatCLI - Coder turn-UI raw-mode toggle (Windows)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"golang.org/x/term"
)

// rawState wraps the previous console mode. The Windows console
// API is different from Unix termios, but golang.org/x/term hides
// the difference behind the same MakeRaw / Restore pair — so the
// shape of the code mirrors the Unix variant for symmetry.
//
// On Win10+ consoles with ENABLE_VIRTUAL_TERMINAL_INPUT, MakeRaw
// also flips the input mode so escape sequences come through to
// the application instead of being intercepted by the console.
// fallback_windows.go's probe already gated the activation on
// ENABLE_VIRTUAL_TERMINAL_PROCESSING for output; the input mode
// follows the same prerequisite.
type rawState struct {
	prev *term.State
}

func enterRawMode(fd int) (*rawState, error) {
	prev, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return &rawState{prev: prev}, nil
}

func (s *rawState) restore(fd int) error {
	if s == nil || s.prev == nil {
		return nil
	}
	return term.Restore(fd, s.prev)
}
