//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package proc

import (
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// ptySupported reports PTY availability on this platform.
const ptySupported = true

// startWithPTY starts cmd attached to a fresh pseudo-terminal and returns
// the master side. pty.Start gives the child its own session (Setsid) with
// the slave as controlling terminal, so the process group id equals the pid
// and the group-signal helpers keep working.
func startWithPTY(cmd *exec.Cmd) (*os.File, error) {
	return pty.Start(cmd)
}
