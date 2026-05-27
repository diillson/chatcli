//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Platform shim for the gateway daemon: Setsid detaches the child from the
 * terminal's process group; SIGTERM stops it; signal(0) probes liveness.
 */
package cli

import (
	"os"
	"syscall"
)

func gatewayDetachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func gatewayTerminate(proc *os.Process) error { return proc.Signal(syscall.SIGTERM) }

func gatewayProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
