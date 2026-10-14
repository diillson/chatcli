//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package utils

import (
	"os"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// OwnsTerminal reports whether this process owns the terminal it would
// reach through /dev/tty: its stdin is that terminal and it runs in the
// terminal's foreground process group.
//
// Every process that inherits a controlling terminal can open /dev/tty —
// including child surfaces that share it without owning it: the `chatcli
// web` process behind /web (stdin is /dev/null), servers, a REPL suspended
// to the background. Changing the terminal's mode, flushing its input or
// injecting keystrokes from such a process breaks the real owner: a coder
// turn run from the browser used to reset the REPL's raw terminal to cooked
// mode, so the next prompt needed extra Enters and echoed every key. Code
// that touches /dev/tty must check this first.
func OwnsTerminal() bool {
	fd := int(os.Stdin.Fd()) // #nosec G115 -- a file descriptor always fits in int
	if !term.IsTerminal(fd) {
		return false
	}
	pgrp, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return false
	}
	return pgrp == unix.Getpgrp()
}
