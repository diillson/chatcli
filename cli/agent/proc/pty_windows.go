//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package proc

import (
	"errors"
	"os"
	"os/exec"
)

// ptySupported reports PTY availability on this platform. ConPTY support is
// deliberately deferred — the Windows terminal stack has bitten before, so
// interactive sessions ship Unix-first with an explicit error here.
const ptySupported = false

// startWithPTY is unavailable on Windows.
func startWithPTY(_ *exec.Cmd) (*os.File, error) {
	return nil, errors.New("interactive PTY sessions are not supported on Windows yet — use @proc start without pty")
}
