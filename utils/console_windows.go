//go:build windows

/*
 * ChatCLI - Windows console bootstrap.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * ChatCLI paints its UI with raw ANSI escapes (spinners, colors, \r\033[K
 * repaints). Windows Terminal interprets them natively, but the legacy
 * console host (cmd.exe / PowerShell outside WT) only does so after the
 * process opts in via ENABLE_VIRTUAL_TERMINAL_PROCESSING. Without the opt-in
 * every escape renders as literal garbage. Enabling it is idempotent and
 * harmless where VT is already on, so main() calls this unconditionally at
 * boot.
 */
package utils

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableVirtualTerminal turns on ANSI/VT escape processing for stdout and
// stderr when they are attached to a Windows console. Redirected handles
// (pipes, files) and consoles that reject the mode (pre-Win10) are skipped
// silently — output degrades exactly as it did before the opt-in existed.
func EnableVirtualTerminal() {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		h := windows.Handle(f.Fd())
		var mode uint32
		if err := windows.GetConsoleMode(h, &mode); err != nil {
			continue // not a console
		}
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
}
