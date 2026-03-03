//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"os"
	"syscall"
	"time"
)

// stdinPollReady waits up to timeout for stdin to have input events available.
// On Windows this uses WaitForSingleObject on the console input handle.
//
// Note: WaitForSingleObject may return true for non-key console events (e.g.,
// window resize). In that case the subsequent os.Stdin.Read may block until
// actual text input arrives. This is acceptable because:
//   - Non-key events are infrequent in typical CLI usage.
//   - The stopStdinReader timeout ensures we don't block indefinitely on exit.
func stdinPollReady(timeout time.Duration) bool {
	h := syscall.Handle(os.Stdin.Fd())
	r, _ := syscall.WaitForSingleObject(h, uint32(timeout.Milliseconds()))
	return r == syscall.WAIT_OBJECT_0
}
