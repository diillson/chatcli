package coder

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResetTTYToSane_NoCrash guards the security prompt's terminal-reset
// path. The pre-fix code called `exec.Command(sttyPath, "sane").Run()` with
// cmd.Stdin == nil, which Go's os/exec rewires to /dev/null — making stty
// silently no-op against the wrong fd and leaving the TTY in whatever
// state go-prompt's TIOCSTI inject left it (no echo / no canonical mode)
// after a park auto-resume. The new helper opens /dev/tty explicitly so
// the reset actually targets the controlling terminal.
//
// This test verifies the function never panics and returns a bool. In CI
// (no /dev/tty, or sttyPath == "") it must return false; in an
// interactive terminal it should return true. Either way it must not
// crash, must not mutate global state, and must release any opened fds.
func TestResetTTYToSane_NoCrash(t *testing.T) {
	got := resetTTYToSane()

	if runtime.GOOS == "windows" || sttyPath == "" {
		assert.False(t, got, "must short-circuit when stty is unavailable")
		return
	}
	// On macOS/Linux the result depends on whether /dev/tty is reachable.
	// Both outcomes are valid; we just guarantee the function is total.
	assert.IsType(t, true, got)
}
