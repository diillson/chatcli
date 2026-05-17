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

// flushTTYInput on Windows discards any unread console input records.
// FlushConsoleInputBuffer empties keyboard, mouse, and focus events. This is
// the closest equivalent to TCIFLUSH on Unix.
//
// If the process has no attached console (e.g., a service or detached
// session), GetStdHandle returns INVALID_HANDLE_VALUE; we treat that as a
// no-op rather than an error, matching the Unix /dev/tty behavior.
func flushTTYInput() error {
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil || h == windows.InvalidHandle {
		return nil
	}
	if err := windows.FlushConsoleInputBuffer(h); err != nil {
		return err
	}
	return nil
}
