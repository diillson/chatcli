//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package proc

import (
	"os/exec"
	"syscall"
)

// shellCommand builds the platform shell invocation for a command line.
func shellCommand(command string) *exec.Cmd {
	return exec.Command("sh", "-c", command) //#nosec G204 -- command vetted by the injected agent validator before reaching here
}

// setProcessGroup puts the child in its own process group so signals reach
// the whole tree (dev servers fork workers).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateGroup sends SIGTERM to the process group (graceful).
func terminateGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// killGroup sends SIGKILL to the process group (forced).
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
