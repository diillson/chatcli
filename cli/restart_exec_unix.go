//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/term"
)

// execRestart replaces this process with bin: same PID, same terminal, same
// job-control slot in the user's shell. Files are opened close-on-exec by
// the Go runtime, so nothing of the old process leaks into the new one.
func execRestart(bin string, argv, env []string) error {
	// go-prompt restores the terminal when the REPL unwinds; "stty sane" is
	// the same safety net the mode switches use, in case a raw mode survived.
	if term.IsTerminal(int(os.Stdin.Fd())) { //#nosec G115 -- a file descriptor fits in int
		cmd := exec.Command("stty", "sane") //#nosec G204 -- fixed argv, no user input
		cmd.Stdin = os.Stdin
		_ = cmd.Run()
	}
	return syscall.Exec(bin, argv, env) //#nosec G204 -- bin is the installed chatcli resolved from argv[0]/PATH
}
