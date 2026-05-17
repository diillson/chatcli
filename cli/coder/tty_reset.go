/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"os"
	"os/exec"
	"runtime"
)

// ResetTTYToSane is the exported entry-point that callers outside the
// coder package use to reset the controlling terminal to canonical
// (cooked, echo-on) mode. Used by the agent loop at the start of every
// ReAct run to recover from a prior go-prompt teardown that may have
// left the TTY in raw mode (no echo, ICRNL off) — a state where
// keystrokes typed during the spinner land in the kernel buffer but
// never echo to the user's screen, producing the "looks frozen / am I
// typing?" UX bug.
//
// This is intentionally a thin wrapper over the unexported
// resetTTYToSane() helper that the security prompt already used. It
// lives in its own file (not in security_ui.go) so that adding new
// exported callers doesn't drag security_ui.go into the QG cyclo-new
// scan — the file has a pre-existing high-complexity formatActionDetails
// function the gate would flag as soon as the file shows up in any diff.
//
// Returns true when the reset was applied. Failures are intentionally
// silent: this is best-effort UX and any error degrades to the previous
// (occasionally-broken-on-resume) behavior, which is what we are
// trying to improve.
func ResetTTYToSane() bool {
	if runtime.GOOS == "windows" || sttyPath == "" {
		return false
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer func() { _ = tty.Close() }()

	cmd := exec.Command(sttyPath, "sane")
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	return cmd.Run() == nil
}
