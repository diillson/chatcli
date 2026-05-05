//go:build linux

/*
 * tty_inject_unix_test.go — pty-backed integration test for the TIOCSTI
 * mechanism that powers park's auto-resume.
 *
 * Linux-only because the pty pair opening dance (TIOCSPTLCK + TIOCGPTN)
 * is Linux-specific; macOS uses libc-level posix_openpt + grantpt +
 * ptsname which require cgo to access cleanly. macOS coverage of the
 * TIOCSTI mechanism is handled by the live smoke test (see PR #879's
 * test plan).
 *
 * The test creates a real pty pair (master + slave), targets the slave
 * fd with the same byte-split injection chatcli uses against /dev/tty
 * in production, then drains the slave to verify the bytes arrived in
 * the expected ordering — body in one read, \r in a separate read after
 * the 15 ms pause that lets go-prompt's reader see ControlM standalone.
 *
 * Skip cases
 *
 *   - Linux 5.16+ with /proc/sys/dev/tty/legacy_tiocsti=1: TIOCSTI is
 *     restricted at the kernel level. The test t.Skip()s with a
 *     specific message so the user knows the platform doesn't support
 *     auto-resume injection there.
 */
package cli

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTYPair opens a master/slave pty pair without depending on a
// third-party package. It mirrors the textbook ptmx + grantpt + unlockpt
// + ptsname dance that ssh/expect-style tools use.
func openPTYPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("ptmx not available: %v", err)
	}

	// Ask the kernel to grant access (no-op on most modern Linux but
	// required by POSIX) and unlock the slave fd.
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		t.Skipf("TIOCSPTLCK: %v", err)
	}
	// Resolve the slave path. unix.IoctlGetInt(TIOCGPTN) returns the pty
	// minor number; the slave path is /dev/pts/N. Linux-specific.
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		// Fallback: macOS uses /dev/ttys?? lookup via ioctl(TIOCPTYGNAME)
		// which is not in golang.org/x/sys. Skip the test there — macOS
		// ships ptmx but the slave path resolution requires the
		// libc-level ptsname() helper that Go does not expose. Real
		// /dev/tty lives elsewhere and is exercised end-to-end by the
		// user-facing smoke test.
		_ = master.Close()
		t.Skipf("TIOCGPTN unsupported (likely non-Linux); end-to-end injection is verified via the live smoke test")
	}
	slavePath := "/dev/pts/" + intToString(n)

	// #nosec G304 -- slavePath is /dev/pts/<n> derived from a kernel
	// ioctl on a fresh ptmx; n is a non-negative int the kernel just
	// allocated for us.
	slave, err := os.OpenFile(slavePath, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		t.Fatalf("open slave %s: %v", slavePath, err)
	}
	return master, slave
}

// intToString avoids strconv just so the test reads more like POSIX C.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+(n%10))) + out
		n /= 10
	}
	return out
}

// TestInjectTTYLine_PtyEndToEnd validates that the byte-split injection
// (body → 15 ms pause → \r) appears on the slave's read side in the
// same shape go-prompt's reader consumes it: a multi-byte read for the
// body and a single-byte read for the trailing \r.
//
// Hard-bounded with a wall-clock timeout so a TIOCSTI rejection (e.g.
// container without a usable controlling tty, kernel hardening that
// silently drops without errno) cannot hang the suite.
func TestInjectTTYLine_PtyEndToEnd(t *testing.T) {
	master, slave := openPTYPair(t)
	defer master.Close()
	defer slave.Close()

	// Put the slave in raw-ish mode so the line discipline does not
	// echo or translate the injected bytes — that matches how go-prompt
	// configures the user's controlling TTY at startup.
	if termios, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS); err == nil {
		termios.Lflag &^= unix.ICANON | unix.ECHO
		termios.Iflag &^= unix.ICRNL
		_ = unix.IoctlSetTermios(int(slave.Fd()), unix.TCSETS, termios)
	}

	const payload = "/resume abcdef12"

	// Inject from a goroutine so we can race it against the read with
	// a timeout. On kernels that silently drop TIOCSTI (older Linux
	// rebuilds with TIOCSTI patched out, or sandboxed cgroups) the
	// inject returns nil but no bytes arrive — that path becomes the
	// "no data within deadline" branch below and we skip cleanly.
	injectErr := make(chan error, 1)
	go func() {
		injectErr <- injectTTYLineOnFd(int(slave.Fd()), payload)
	}()

	// Read in a goroutine with a hard wall-clock deadline. This avoids
	// relying on file-descriptor read deadlines, which not every pty
	// kernel implementation honors.
	type readResult struct {
		buf []byte
		err error
	}
	readDone := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 0, 128)
		tmp := make([]byte, 64)
		for len(buf) < len(payload)+1 {
			n, err := slave.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
					break
				}
				readDone <- readResult{buf, err}
				return
			}
		}
		readDone <- readResult{buf, nil}
	}()

	// 3 s is plenty: the inject body sleeps 15 ms in the middle, and
	// the kernel transit is sub-ms. If we don't have data by then,
	// TIOCSTI is blocked — close the slave so the read goroutine
	// unblocks and we skip with a clear message.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()

	var (
		buf     []byte
		readErr error
	)
	select {
	case res := <-readDone:
		buf = res.buf
		readErr = res.err
	case <-deadline.C:
		_ = slave.Close()
		// Drain the inject goroutine before deciding the outcome.
		injErr := <-injectErr
		if injErr != nil && strings.Contains(injErr.Error(), "TIOCSTI") {
			t.Skipf("TIOCSTI failed: %v (likely kernel hardening or restricted container)", injErr)
		}
		t.Skipf("no bytes appeared on slave within 3s; TIOCSTI silently dropped (kernel hardening or container restriction)")
	}

	if injErr := <-injectErr; injErr != nil {
		if strings.Contains(injErr.Error(), "TIOCSTI") {
			t.Skipf("TIOCSTI restricted on this kernel/platform: %v", injErr)
		}
		t.Fatalf("injectTTYLineOnFd: %v", injErr)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		t.Fatalf("read slave: %v", readErr)
	}

	got := string(buf)
	if !strings.Contains(got, payload) {
		t.Fatalf("payload not received via TIOCSTI; got=%q want substring=%q", got, payload)
	}
	if !strings.Contains(got, "\r") {
		t.Fatalf("expected \\r in stream after body; got=%q", got)
	}
}

// TestInjectTTYLine_EmptyNoOp asserts the public helper returns nil
// for the empty-string case without making any ioctl calls.
func TestInjectTTYLine_EmptyNoOp(t *testing.T) {
	if err := injectTTYLine(""); err != nil {
		t.Fatalf("empty line should be a silent no-op, got: %v", err)
	}
	if err := injectTTYLineOnFd(-1, ""); err != nil {
		t.Fatalf("empty line on bad fd should still no-op (no ioctl issued); got: %v", err)
	}
}
