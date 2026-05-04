//go:build windows

/*
 * tty_inject_windows.go — Windows stub for the TIOCSTI mechanism.
 *
 * Windows has no direct equivalent: the conhost / Windows Terminal
 * input pipe is not addressable via ioctl, and WriteConsoleInput
 * targets a console handle whose buffer is owned by go-prompt's raw
 * reader. Reliable cross-handle injection would require a console
 * subsystem rewrite that go-prompt does not currently support.
 *
 * The function returns errTTYInjectUnsupported so the bridge logs the
 * limitation; the executor-hook drain in cli.executor still consumes
 * the resume queue when the user types any character + Enter.
 */
package cli

import "errors"

var errTTYInjectUnsupported = errors.New("tty inject: TIOCSTI not supported on Windows")

func injectTTYLine(_ string) error {
	return errTTYInjectUnsupported
}
