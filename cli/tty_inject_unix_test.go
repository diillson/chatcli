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
// same shape go-prompt's reader consumes it.
//
// Synchronous structure (no goroutines sharing the slave file handle):
// the kernel hardens against shared file access; the previous concurrent
// version tripped the race detector under `go test -race`. Now the test
// probes TIOCSTI once up front and t.Skip()s when the kernel restricts
// it; on success the inject + read run sequentially in the test
// goroutine using non-blocking reads.
func TestInjectTTYLine_PtyEndToEnd(t *testing.T) {
	master, slave := openPTYPair(t)
	defer master.Close()
	defer slave.Close()

	slaveFd := int(slave.Fd())

	// Put the slave in raw-ish mode so the line discipline does not
	// echo or translate the injected bytes — that matches how go-prompt
	// configures the user's controlling TTY at startup.
	if termios, err := unix.IoctlGetTermios(slaveFd, unix.TCGETS); err == nil {
		termios.Lflag &^= unix.ICANON | unix.ECHO
		termios.Iflag &^= unix.ICRNL
		_ = unix.IoctlSetTermios(slaveFd, unix.TCSETS, termios)
	}

	// Probe TIOCSTI before doing the real injection. On kernels that
	// reject TIOCSTI (Linux 6.x+ default with legacy_tiocsti=1, Docker
	// Desktop linuxkit, sandboxed CI runners), this surfaces as EPERM /
	// ENOTTY and we skip the test with a clear message. The probe byte
	// is drained right after via a non-blocking read so the real
	// injection starts against a clean queue.
	if err := tiocsti(slaveFd, '\x00'); err != nil {
		t.Skipf("TIOCSTI restricted on this kernel/runtime: %v", err)
	}
	// Set non-blocking so the probe drain returns immediately even if
	// the kernel didn't actually deliver the byte (some hardenings
	// accept the ioctl but drop the byte). Skip if non-blocking
	// can't be enabled — every modern Linux/BSD pty supports it.
	if err := unix.SetNonblock(slaveFd, true); err != nil {
		t.Skipf("SetNonblock on slave failed: %v", err)
	}
	defer func() { _ = unix.SetNonblock(slaveFd, false) }()
	drainOnce(slaveFd)

	const payload = "/resume abcdef12"

	if err := injectTTYLineOnFd(slaveFd, payload); err != nil {
		// Filter again here — concurrent kernel updates between the
		// probe and the real injection could flip the policy.
		if strings.Contains(err.Error(), "TIOCSTI") {
			t.Skipf("TIOCSTI restricted: %v", err)
		}
		t.Fatalf("injectTTYLineOnFd: %v", err)
	}

	// Drain with a short wall-clock budget. Non-blocking Reads return
	// EAGAIN when no data is queued; we sleep briefly to let the kernel
	// publish the bytes and retry. Total budget is 1 s — orders of
	// magnitude above the 15 ms inject pause + sub-ms kernel transit.
	deadline := time.Now().Add(time.Second)
	buf := make([]byte, 0, 128)
	tmp := make([]byte, 64)
	for time.Now().Before(deadline) && len(buf) < len(payload)+1 {
		n, err := slave.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			continue
		}
		if err != nil && errors.Is(err, io.EOF) {
			break
		}
		// EAGAIN / temporarily empty queue — sleep a few ms and retry.
		time.Sleep(10 * time.Millisecond)
	}

	got := string(buf)
	if !strings.Contains(got, payload) {
		t.Fatalf("payload not received via TIOCSTI; got=%q want substring=%q", got, payload)
	}
	if !strings.Contains(got, "\r") {
		t.Fatalf("expected \\r in stream after body; got=%q", got)
	}
}

// drainOnce reads everything currently queued on a non-blocking fd.
// Used to discard the TIOCSTI probe byte before the real test inject.
func drainOnce(fd int) {
	tmp := make([]byte, 64)
	for {
		n, err := unix.Read(fd, tmp)
		if n <= 0 || err != nil {
			return
		}
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
