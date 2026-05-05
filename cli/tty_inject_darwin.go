//go:build darwin || freebsd || netbsd || openbsd

package cli

import (
	"syscall"
	"unsafe"
)

// tiocsti sends a single byte to the controlling TTY's input buffer
// via the TIOCSTI ioctl. The BSD-family ioctl signature wants a
// pointer to a single byte; we route through raw syscall because
// golang.org/x/sys/unix exposes TIOCSTI as a constant on Darwin but
// without a typed helper for the pointer-to-byte form.
//
// Returns the underlying errno on failure. The caller maps EPERM /
// ENOTTY into errTTYInjectUnsupported.
//
// macOS specific: since macOS Ventura, kern.tiocsti_disable=1 is the
// default, which makes this call return EPERM. Users who want auto-
// resume on macOS need 'sudo sysctl -w kern.tiocsti_disable=0' (and
// understand that this re-enables a feature Apple disabled to mitigate
// shell-injection attacks against TUIs that read from /dev/tty).
func tiocsti(fd int, b byte) error {
	const TIOCSTI = 0x80017472 // (BSD: 'I' << 8 | 0x72) | (1 << 31) | (1 << 30)
	// #nosec G103,G115 -- G103: TIOCSTI ioctl signature requires a
	// pointer-to-byte; b is a stack-allocated function parameter
	// whose lifetime fully covers the syscall, and the kernel copies
	// the byte synchronously before returning. No GC interaction.
	// G115: fd is a process-local FD number, always non-negative
	// and within int range; conversion to uintptr cannot overflow.
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(TIOCSTI),
		uintptr(unsafe.Pointer(&b)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}
