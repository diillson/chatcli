//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"errors"
	"syscall"
)

// parkProcessAlive reports whether pid is a running process. A signal the
// kernel refuses for lack of permission (EPERM) went to a process that
// EXISTS under another user — a park dir shared with a service account or
// a sudo'd chatcli — and reading that as "gone" deleted a waiting park.
// Only "no such process" is dead.
func parkProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
