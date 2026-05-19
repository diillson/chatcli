//go:build !windows

/*
 * ChatCLI - Coder turn-UI raw-mode toggle (Unix)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"golang.org/x/term"
)

// rawState wraps the previous TTY state. On Unix this is the cooked-
// mode termios snapshot returned by term.MakeRaw / GetState. The
// caller stashes it on the TurnUI and restores it on End or on a
// signal-driven cleanup.
//
// We use golang.org/x/term rather than rolling our own ioctl(TIOCGETA /
// TIOCSETA) because term already handles the platform differences
// across Linux / macOS / *BSD and is the same dependency the rest of
// chatcli uses for raw-mode entry in go-prompt-adjacent paths. Keeping
// the dependency footprint stable matters for downstream packagers.
type rawState struct {
	prev *term.State
}

// enterRawMode puts the given file descriptor into raw mode (ICANON
// off, ECHO off, ISIG off, plus the related flag clears that
// term.MakeRaw applies) and returns a rawState that knows how to
// undo it. A non-TTY fd (or any underlying ioctl failure) returns an
// error; the caller is expected to fall back to the cooked legacy
// renderer rather than proceed with a half-applied raw mode.
func enterRawMode(fd int) (*rawState, error) {
	prev, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return &rawState{prev: prev}, nil
}

// restore returns the fd to the cooked-mode state captured by
// enterRawMode. Safe to call on a nil receiver — the input-loop
// cleanup path constructs the deferred restore before knowing
// whether enterRawMode succeeded, so a nil rawState must be a no-op
// instead of a panic.
func (s *rawState) restore(fd int) error {
	if s == nil || s.prev == nil {
		return nil
	}
	return term.Restore(fd, s.prev)
}
