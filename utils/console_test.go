/*
 * ChatCLI - Console bootstrap tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import "testing"

// TestEnableVirtualTerminalIsSafe exercises the console opt-in on every
// platform: a no-op on Unix, and on Windows it must tolerate redirected
// handles (the test binary's stdout is usually a pipe) without panicking
// or returning anything — the contract is "best effort, never fails".
func TestEnableVirtualTerminalIsSafe(t *testing.T) {
	EnableVirtualTerminal()
	EnableVirtualTerminal() // idempotent by contract
}
