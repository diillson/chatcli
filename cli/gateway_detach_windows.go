//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Platform shim for the gateway daemon: CREATE_NEW_PROCESS_GROUP detaches the
 * child from the parent console; Kill stops it. Windows os.FindProcess always
 * succeeds, so liveness is best-effort.
 */
package cli

import (
	"os"
	"syscall"
)

func gatewayDetachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000200} // CREATE_NEW_PROCESS_GROUP
}

func gatewayTerminate(proc *os.Process) error { return proc.Kill() }

func gatewayProcessAlive(pid int) bool {
	_, err := os.FindProcess(pid)
	return err == nil
}
