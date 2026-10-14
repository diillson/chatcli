//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package utils

import (
	"os"

	"golang.org/x/term"
)

// OwnsTerminal reports whether this process owns the console it would
// touch: its stdin is that console. Child surfaces that share the console
// without owning it (the `chatcli web` process behind /web, servers) run
// with a redirected stdin and must never change the console mode.
func OwnsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) // #nosec G115 -- a file descriptor always fits in int
}
