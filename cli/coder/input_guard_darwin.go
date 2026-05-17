//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package coder

import (
	"os"

	"golang.org/x/sys/unix"
)

// flushTTYInput on BSD-family systems uses TIOCFLUSH with the FREAD bit set
// to discard pending input. Unlike Linux's TCFLSH, the argument is a bitmask
// of FREAD/FWRITE, not a queue selector.
//
// The constant is hardcoded because golang.org/x/sys/unix does not export
// FREAD on Darwin (it lives in <sys/file.h>, not <sys/termios.h>). The value
// FREAD == 0x0001 is stable across all BSD-derived systems we target. By
// coincidence the same numeric value is exposed as unix.TCIFLUSH on Darwin,
// which we use as a self-documenting alias.
func flushTTYInput() error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = tty.Close() }()
	if err := unix.IoctlSetPointerInt(int(tty.Fd()), unix.TIOCFLUSH, unix.TCIFLUSH); err != nil {
		return err
	}
	return nil
}
