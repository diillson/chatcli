//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// stdinPollReady waits up to timeout for stdin to have data available for
// reading. On Unix (Linux, macOS) this uses poll(2) which correctly handles
// TTY file descriptors — unlike os.File.SetReadDeadline which silently fails
// on macOS TTY stdin.
func stdinPollReady(timeout time.Duration) bool {
	fds := []unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(timeout.Milliseconds()))
	return err == nil && n > 0
}
