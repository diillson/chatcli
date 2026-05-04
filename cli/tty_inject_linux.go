//go:build linux

package cli

import "golang.org/x/sys/unix"

// tiocsti sends a single byte to the controlling TTY's input buffer
// via the TIOCSTI ioctl. On Linux, IoctlSetPointerInt accepts the
// ioctl number and a *int containing the byte value (low 8 bits).
func tiocsti(fd int, b byte) error {
	v := int(b)
	return unix.IoctlSetPointerInt(fd, unix.TIOCSTI, v)
}
