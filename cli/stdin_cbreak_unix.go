//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// enableStdinCbreak switches the controlling TTY to a cbreak-style mode for
// the centralized stdin reader: canonical line buffering and kernel echo go
// OFF, signal generation (ISIG) stays ON so Ctrl+C keeps working. In cooked
// mode the kernel held typed bytes until Enter and echoed them on top of
// the spinner line, where the 100ms repaint ate them — the user typed
// blind. In cbreak the reader receives bytes as they are typed and the
// live display renders the in-flight line itself.
//
// Returns a restore func (never nil) that reinstates the exact prior
// state captured with `stty -g`. Non-TTY stdin (gateway, tests, pipes) is
// a no-op.
func enableStdinCbreak() func() {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return func() {}
	}
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return func() {}
	}
	saved, err := sttyOutput(tty, "-g")
	if err != nil || strings.TrimSpace(saved) == "" {
		_ = tty.Close()
		return func() {}
	}
	if err := sttyRun(tty, "-icanon", "-echo", "min", "1", "time", "0"); err != nil {
		_ = tty.Close()
		return func() {}
	}
	savedState := strings.TrimSpace(saved)
	return func() {
		_ = sttyRun(tty, savedState)
		_ = tty.Close()
	}
}

// sttyOutput runs stty against the tty and returns its stdout.
func sttyOutput(tty *os.File, args ...string) (string, error) {
	cmd := exec.Command("stty", args...) // #nosec G204 -- fixed binary; args are stty mode tokens or the state string previously emitted by `stty -g` on this same tty, never user input
	cmd.Stdin = tty
	out, err := cmd.Output()
	return string(out), err
}

// sttyRun runs stty against the tty, discarding output.
func sttyRun(tty *os.File, args ...string) error {
	cmd := exec.Command("stty", args...) // #nosec G204 -- fixed binary; args are stty mode tokens or the state string previously emitted by `stty -g` on this same tty, never user input
	cmd.Stdin = tty
	return cmd.Run()
}
