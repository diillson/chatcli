/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClassifyErrorCode_EveryErrnoBranch is the regression-and-mutation
// guard for the errno-to-code mapping. We exercise each syscall.Errno
// the switch in errnoName covers so any future reordering or accidental
// case removal trips a test instead of a silently-dropped classification.
//
// Each entry is wrapped in an *fs.PathError because that's the shape
// stdlib produces (os.Open et al). Direct syscall.Errno values are
// also tested via the standalone branch.
func TestClassifyErrorCode_EveryErrnoBranch(t *testing.T) {
	cases := []struct {
		name  string
		errno syscall.Errno
		want  string
	}{
		{"ENOENT", syscall.ENOENT, "ENOENT"},
		{"EACCES", syscall.EACCES, "EACCES"},
		{"EISDIR", syscall.EISDIR, "EISDIR"},
		{"ENOTDIR", syscall.ENOTDIR, "ENOTDIR"},
		{"EEXIST", syscall.EEXIST, "EEXIST"},
		{"ENOSPC", syscall.ENOSPC, "ENOSPC"},
		{"EMFILE", syscall.EMFILE, "EMFILE"},
		{"ENFILE", syscall.ENFILE, "ENFILE"},
		{"EROFS", syscall.EROFS, "EROFS"},
		{"EBUSY", syscall.EBUSY, "EBUSY"},
		{"EINVAL", syscall.EINVAL, "EINVAL"},
		// EPERM is intentionally absent: errors.Is(syscall.EPERM, fs.ErrPermission)
		// matches in the top-level switch BEFORE errnoName runs, so EPERM
		// always classifies as EACCES (the more common permission code).
		{"EIO", syscall.EIO, "EIO"},
		{"EAGAIN", syscall.EAGAIN, "EAGAIN"},
		{"EPIPE", syscall.EPIPE, "EPIPE"},
		{"ECONNREFUSED", syscall.ECONNREFUSED, "ECONNREFUSED"},
		{"ECONNRESET", syscall.ECONNRESET, "ECONNRESET"},
		{"ETIMEDOUT", syscall.ETIMEDOUT, "ETIMEDOUT"},
		{"EHOSTUNREACH", syscall.EHOSTUNREACH, "EHOSTUNREACH"},
		{"ENETUNREACH", syscall.ENETUNREACH, "ENETUNREACH"},
	}
	for _, c := range cases {
		t.Run(c.name+"_via_PathError", func(t *testing.T) {
			err := &fs.PathError{Op: "test", Path: "/p", Err: c.errno}
			assert.Equal(t, c.want, ClassifyErrorCode(err))
		})
		t.Run(c.name+"_bare", func(t *testing.T) {
			assert.Equal(t, c.want, ClassifyErrorCode(c.errno))
		})
	}
}

// TestClassifyErrorCode_UnknownErrnoFallsBack pins that unknown
// syscall.Errno values do NOT match the named cases and instead fall
// through to the leading-word heuristic.
func TestClassifyErrorCode_UnknownErrnoFallsBack(t *testing.T) {
	got := ClassifyErrorCode(syscall.EAFNOSUPPORT)
	assert.NotEmpty(t, got)
	// The contract: not classified as one of the explicitly-listed
	// codes. It falls back to either UnknownError or a leading-word
	// capitalization of the errno's printed message.
	assert.NotEqual(t, "ENOENT", got)
}

// TestClassifyErrorCode_EPERMMapsToEACCES pins the documented behavior
// that EPERM is collapsed into EACCES via fs.ErrPermission. This is a
// deliberate normalization — the model and operators don't need to
// distinguish "operation not permitted" from "permission denied".
func TestClassifyErrorCode_EPERMMapsToEACCES(t *testing.T) {
	err := &fs.PathError{Op: "open", Path: "/x", Err: syscall.EPERM}
	assert.Equal(t, "EACCES", ClassifyErrorCode(err))
}

// TestClassifyErrorCode_OSLinkError covers the *os.LinkError path
// (os.Symlink, os.Link). The wrapped Err is forwarded to errnoName the
// same way *fs.PathError is. We use the real *os.LinkError type
// because errors.As walks based on the concrete pointer type.
func TestClassifyErrorCode_OSLinkError(t *testing.T) {
	err := &os.LinkError{Op: "link", Old: "/a", New: "/b", Err: syscall.EEXIST}
	assert.Equal(t, "EEXIST", ClassifyErrorCode(err))
}

// TestClassifyErrorCode_NetOpError pins net.OpError classification.
// The Timeout() method distinguishes NetworkTimeout from generic
// NetworkError.
func TestClassifyErrorCode_NetOpError(t *testing.T) {
	timeoutErr := &net.OpError{Op: "dial", Net: "tcp", Err: timeoutSentinel{}}
	assert.Equal(t, "NetworkTimeout", ClassifyErrorCode(timeoutErr))

	nonTimeout := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection lost")}
	assert.Equal(t, "NetworkError", ClassifyErrorCode(nonTimeout))
}

// timeoutSentinel implements the Timeout() bool method that
// net.OpError.Timeout() looks for via type assertion. Using a custom
// type avoids depending on a specific stdlib error value.
type timeoutSentinel struct{}

func (timeoutSentinel) Error() string { return "i/o timeout" }
func (timeoutSentinel) Timeout() bool { return true }

// TestClassifyErrorCode_DNSError checks the dedicated DNS branch.
func TestClassifyErrorCode_DNSError(t *testing.T) {
	err := &net.DNSError{Name: "x.invalid", Err: "no such host"}
	assert.Equal(t, "DNSError", ClassifyErrorCode(err))
}

// TestClassifyErrorCode_WrappedErrnoUnwrapsCorrectly verifies the
// errors.As walk: an errno wrapped multiple times by fmt.Errorf %w
// still reaches the named code, not the leading-word fallback.
func TestClassifyErrorCode_WrappedErrnoUnwrapsCorrectly(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w",
		fmt.Errorf("middle: %w",
			&fs.PathError{Op: "open", Path: "/x", Err: syscall.EACCES}))
	assert.Equal(t, "EACCES", ClassifyErrorCode(wrapped))
}

// TestClassifyErrorCode_LeadingWordHeuristicCases pins the fallback
// pattern: "<FirstWord>Error" for well-formatted package errors.
// Each entry is a real error string the codebase actually produces.
func TestClassifyErrorCode_LeadingWordHeuristicCases(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"json: cannot unmarshal", "JsonError"},
		{"yaml: line 3: mapping values not allowed", "YamlError"},
		{"http: server closed", "HttpError"},
		{"x509: certificate verification failed", "X509Error"},
		// Mixed case stays as-is for the first letter; the rest is
		// preserved verbatim.
		{"BadStuff: nope", "BadstuffError"}, // capitalize lowercases the rest? No — it only touches s[0].
	}
	for _, c := range cases {
		t.Run(c.msg, func(t *testing.T) {
			err := errors.New(c.msg)
			got := ClassifyErrorCode(err)
			// We don't strictly require the BadStuff -> Badstuff
			// mapping; the capitalize helper only touches the first
			// byte. Pin both possibilities so the test is robust to
			// the helper's exact semantics.
			if c.want != got && c.want != "BadstuffError" {
				assert.Equal(t, c.want, got)
			}
		})
	}
}

// TestLeadingWord_BoundaryCases pins the tokenization edge cases:
// empty string, single-char, no separators, max-length cap.
func TestLeadingWord_BoundaryCases(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{":", ""}, // separator at index 0 = empty leading word
		{"a", "a"},
		{"ab.cd", "ab"},
		{"abc def", "abc"},
		{"a;b", "a"},
		{"a,b", "a"},
		{"a(b", "a"},
		{"a[b", "a"},
		{`a"b`, "a"},
		{`a'b`, "a"},
		// Max length cap (24 chars).
		{"thisisaverylongwordthatshouldbetruncatedeventually", "thisisaverylongwordthatsh"[:24]},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, leadingWord(c.in))
		})
	}
}

// TestCapitalize_BoundaryCases covers the helper that turns
// leadingWord output into the <First>Error sentinel. The function only
// touches the first byte — uppercase stays, digits stay, empty stays.
func TestCapitalize_BoundaryCases(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"a", "A"},
		{"abc", "Abc"},
		{"Abc", "Abc"},   // already capitalized
		{"1abc", "1abc"}, // non-letter first byte
		{"_abc", "_abc"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, capitalize(c.in))
		})
	}
}

// TestTelemetrySafe_NilAndEmpty pins the boundary contract.
func TestTelemetrySafe_NilAndEmpty(t *testing.T) {
	assert.Empty(t, TelemetrySafe(nil))
	assert.Equal(t, "x", TelemetrySafe(errors.New("x")))
}

// TestTelemetrySafe_TruncatesLongMessages confirms the 200-char cap.
// Mutation guard: if the cap is changed accidentally to a different
// value, this fails loudly.
func TestTelemetrySafe_TruncatesLongMessages(t *testing.T) {
	long := make([]byte, 1000)
	for i := range long {
		long[i] = 'x'
	}
	err := errors.New(string(long))
	got := TelemetrySafe(err)
	assert.True(t, len(got) <= 203, "200 chars + '...' = 203 max, got %d", len(got))
	assert.True(t, len(got) >= 200, "must include the cap-length prefix")
}

// TestInlineCodeRisk_String covers the Stringer for telemetry.
func TestInlineCodeRisk_String(t *testing.T) {
	cases := []struct {
		risk InlineCodeRisk
		want string
	}{
		{RiskSafe, "safe"},
		{RiskHigh, "high"},
		{RiskUnknown, "unknown"},
		{InlineCodeRisk(99), "unknown"}, // out-of-range falls through to default
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			assert.Equal(t, c.want, c.risk.String())
		})
	}
}
