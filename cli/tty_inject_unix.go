//go:build !windows

/*
 * tty_inject_unix.go — controlling-TTY input injection via TIOCSTI.
 *
 * Used by the park subsystem to wake go-prompt out of its blocking
 * stdin read when a parked agent's resume becomes ready. We inject
 * the literal string "/resume <token>\n" as if the user typed it; the
 * executor then runs naturally (drainPendingResumes consumes the queue,
 * the resume runs in foreground with full terminal control), the same
 * code path /coder uses to take the terminal back from go-prompt.
 *
 * Platform notes
 *
 *   - Linux: TIOCSTI works for the controlling user's TTY without
 *     elevated privileges. Returns EPERM only if the calling process
 *     is in a different session.
 *
 *   - macOS: TIOCSTI was deprecated in macOS Ventura and is gated
 *     behind kern.tiocsti_disable=0 on most modern installs. Calls
 *     return EPERM under the default policy. We surface the error so
 *     the bridge can log the limitation; the executor-hook drain in
 *     cli.executor still consumes the queue when the user types any
 *     character + Enter.
 *
 *   - FreeBSD/NetBSD/OpenBSD: TIOCSTI works for controlling TTYs in
 *     the same session. macOS-style restrictions don't apply.
 */
package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// errTTYInjectUnsupported indicates the platform / OS configuration
// rejected the TIOCSTI ioctl. The caller logs this at debug level and
// falls back to the queue-based drain path.
var errTTYInjectUnsupported = errors.New("tty inject: TIOCSTI not permitted on this platform")

// injectTTYLine writes line + "\n" to the controlling TTY's input
// buffer one byte at a time via TIOCSTI. The receiver (go-prompt's
// raw stdin reader) sees the bytes as if the user had typed them.
//
// We pick the file descriptor in this priority:
//  1. /dev/tty (always the controlling TTY when one exists)
//  2. os.Stdin (covers the rare case where /dev/tty is unavailable)
//
// /dev/tty is preferred because it bypasses any stdin redirection
// the user may have applied (e.g. running chatcli inside a wrapper
// that pipes stdin).
func injectTTYLine(line string) error {
	if line == "" {
		return nil
	}
	fd, closer, err := openControllingTTY()
	if err != nil {
		return fmt.Errorf("tty inject: %w", err)
	}
	defer closer()

	// go-prompt parses each Read() call as a single key event, mapping
	// the WHOLE byte slice via bytes.Equal against its ASCII table. A
	// multi-byte read does not match any control sequence, so the entry
	// hits the default branch and gets inserted as text. That means
	// dumping "/resume <token>\r" all at once via TIOCSTI lands on the
	// prompt as literal characters — including the trailing \r — and
	// the line is never submitted.
	//
	// Workaround: split the injection into two TIOCSTI bursts.
	//
	//   1. The command body. go-prompt reads it as a single multi-byte
	//      buffer, falls into the default branch, and InsertText writes
	//      the chars into the line editor.
	//
	//   2. A 15 ms pause — go-prompt's readBuffer goroutine sleeps 10 ms
	//      between Read() calls, so 15 ms guarantees the next read sees
	//      a fresh single-byte buffer.
	//
	//   3. A lone \r byte. With len(b)==1 and b[0]==0x0d, GetKey
	//      classifies it as ControlM, which feed() routes to the
	//      submit branch (case Enter, ControlJ, ControlM).
	//
	// Net effect: the same UX as a real key press, achieved without
	// modifying go-prompt.
	body := []byte(line)
	for _, b := range body {
		if err := writeOneByte(fd, b); err != nil {
			return err
		}
	}
	time.Sleep(15 * time.Millisecond)
	if err := writeOneByte(fd, '\r'); err != nil {
		return err
	}
	return nil
}

// openControllingTTY tries /dev/tty first, falling back to stdin.
// Returns the fd, a closer (no-op when stdin is reused), or an error.
func openControllingTTY() (int, func(), error) {
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		fd := int(f.Fd())
		return fd, func() { _ = f.Close() }, nil
	}
	return int(os.Stdin.Fd()), func() {}, nil
}

// writeOneByte issues a single TIOCSTI ioctl. Wraps EPERM/ENOTTY into
// errTTYInjectUnsupported so callers can detect the platform-disabled
// case without parsing errno values.
func writeOneByte(fd int, b byte) error {
	if err := tiocsti(fd, b); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("%w: %v", errTTYInjectUnsupported, err)
		}
		return fmt.Errorf("tty inject byte: %w", err)
	}
	return nil
}
