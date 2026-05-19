//go:build windows

/*
 * ChatCLI - Coder turn-UI Windows ANSI probe
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"golang.org/x/sys/windows"
)

// windowsAnsiAvailableImpl probes whether the stdout console handle
// has virtual-terminal processing enabled, which is the minimum
// requirement for the cursor positioning and DECSTBM sequences the
// split UI emits. Win10 1607+ supports the mode but it is opt-in per
// console — older consoles (cmd.exe on Win7/8) and some terminal
// emulators that proxy stdout through pipes will return false.
//
// On any GetStdHandle / GetConsoleMode failure we conservatively return
// false: drawing escape codes into a console that prints them as
// literals would leave the user with a wedged terminal full of "[?1049h"
// soup.
func windowsAnsiAvailableImpl() bool {
	h, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return false
	}
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	return mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0
}
