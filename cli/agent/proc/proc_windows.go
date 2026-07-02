//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package proc

import "os/exec"

// shellCommand builds the platform shell invocation for a command line.
func shellCommand(command string) *exec.Cmd {
	return exec.Command("cmd", "/C", command) //#nosec G204 -- command vetted by the injected agent validator before reaching here
}

// setProcessGroup is a no-op on Windows; process-tree termination goes
// through taskkill in killGroup.
func setProcessGroup(_ *exec.Cmd) {}

// terminateGroup asks the process tree to stop. Windows has no SIGTERM
// equivalent for console children we don't own, so both paths use taskkill;
// the graceful variant omits /F.
func terminateGroup(cmd *exec.Cmd) {
	taskkill(cmd, false)
}

// killGroup force-kills the process tree.
func killGroup(cmd *exec.Cmd) {
	taskkill(cmd, true)
}

func taskkill(cmd *exec.Cmd, force bool) {
	if cmd.Process == nil {
		return
	}
	args := []string{"/T", "/PID", itoa(cmd.Process.Pid)}
	if force {
		args = append([]string{"/F"}, args...)
	}
	_ = exec.Command("taskkill", args...).Run() //#nosec G204 -- fixed binary, numeric pid
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
