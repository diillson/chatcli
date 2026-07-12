//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"golang.org/x/sys/windows"
)

// restoreCookedModeWindows is the Windows counterpart of `stty sane`: it
// resets the console input buffer to canonical (cooked, echo-on) mode. The
// console mode is a property of the shared input buffer, not of a single
// handle, so a raw mode left behind by a dirty go-prompt / Bubble Tea
// teardown persists for every subsequent reader — in cooked-reader flows
// (security confirmations, the centralized stdin reader) that surfaces as
// keystrokes that never echo and lines that never complete.
func restoreCookedModeWindows() bool {
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil || h == windows.InvalidHandle {
		return false
	}
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		// Not a console (piped/redirected stdin) — nothing to restore.
		return false
	}
	cooked := mode | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT
	// VT input turns keys into escape sequences, which the cooked line
	// editor would insert literally; raw-mode readers re-enable it themselves.
	cooked &^= windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if cooked == mode {
		return true
	}
	return windows.SetConsoleMode(h, cooked) == nil
}
