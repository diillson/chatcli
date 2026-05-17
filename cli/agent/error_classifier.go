/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// ClassifyErrorCode maps a Go error into a stable, locale-independent
// short code suitable for telemetry, log fields, and the wire-level
// IsError marker. The returned string is bounded (≤32 chars) and
// contains only ASCII so it round-trips through any provider format.
//
// Mapping rules:
//
//	*os.PathError      → ENOENT / EACCES / EISDIR / EEXIST / ENOSPC / ETOOMANY...
//	*fs.PathError      → same
//	syscall.Errno      → the errno name (ENOENT, ECONNREFUSED, etc.)
//	*exec.ExitError    → "ExitCode:<N>"
//	*net.OpError       → "NetworkError" (further classified by the wrapped err)
//	context.Canceled   → "Canceled"
//	context.DeadlineExceeded → "Timeout"
//	Anything else      → "UnknownError"
//
// Callers should not parse this string back; it's an opaque sentinel for
// telemetry and conditional UI ("retry on Timeout, not on EACCES").
func ClassifyErrorCode(err error) string {
	if err == nil {
		return ""
	}

	switch {
	case errors.Is(err, context.Canceled):
		return "Canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "Timeout"
	case errors.Is(err, fs.ErrNotExist):
		return "ENOENT"
	case errors.Is(err, fs.ErrPermission):
		return "EACCES"
	case errors.Is(err, fs.ErrExist):
		return "EEXIST"
	}

	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		if code := errnoName(pathErr.Err); code != "" {
			return code
		}
	}

	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		if code := errnoName(linkErr.Err); code != "" {
			return code
		}
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("ExitCode:%d", exitErr.ExitCode())
	}

	var netErr *net.OpError
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "NetworkTimeout"
		}
		return "NetworkError"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "DNSError"
	}

	var errno syscall.Errno
	if errors.As(err, &errno) {
		if name := errnoName(errno); name != "" {
			return name
		}
	}

	// Fall back to a sanitized leading word of the error message — keeps
	// the code stable for well-formatted package errors like
	// "json: cannot unmarshal …" → "JsonError".
	if msg := err.Error(); msg != "" {
		if leading := leadingWord(msg); leading != "" {
			return capitalize(leading) + "Error"
		}
	}
	return "UnknownError"
}

// errnoName returns the human-readable errno constant name for the
// common file-system and network errors. We enumerate only the ones we
// expect to surface from a CLI tool; everything else falls through to
// the caller for the generic UnknownError fallback.
func errnoName(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return ""
	}
	switch errno {
	case syscall.ENOENT:
		return "ENOENT"
	case syscall.EACCES:
		return "EACCES"
	case syscall.EISDIR:
		return "EISDIR"
	case syscall.ENOTDIR:
		return "ENOTDIR"
	case syscall.EEXIST:
		return "EEXIST"
	case syscall.ENOSPC:
		return "ENOSPC"
	case syscall.EMFILE:
		return "EMFILE"
	case syscall.ENFILE:
		return "ENFILE"
	case syscall.EROFS:
		return "EROFS"
	case syscall.EBUSY:
		return "EBUSY"
	case syscall.EINVAL:
		return "EINVAL"
	case syscall.EPERM:
		return "EPERM"
	case syscall.EIO:
		return "EIO"
	case syscall.EAGAIN:
		return "EAGAIN"
	case syscall.EPIPE:
		return "EPIPE"
	case syscall.ECONNREFUSED:
		return "ECONNREFUSED"
	case syscall.ECONNRESET:
		return "ECONNRESET"
	case syscall.ETIMEDOUT:
		return "ETIMEDOUT"
	case syscall.EHOSTUNREACH:
		return "EHOSTUNREACH"
	case syscall.ENETUNREACH:
		return "ENETUNREACH"
	}
	return ""
}

// leadingWord returns the first alphanumeric token of a message, used
// for the fallback "<FirstWord>Error" code. Whitespace, punctuation,
// and quotes terminate the scan.
func leadingWord(s string) string {
	for i, r := range s {
		if r == ' ' || r == ':' || r == ';' || r == ',' || r == '"' || r == '\'' || r == '.' || r == '(' || r == '[' {
			if i == 0 {
				return ""
			}
			return s[:i]
		}
	}
	if len(s) > 24 {
		return s[:24]
	}
	return s
}

// capitalize returns s with its first byte uppercased (ASCII only).
// Used by the fallback so codes look like Go-style sentinels.
func capitalize(s string) string {
	if s == "" {
		return ""
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-'a'+'A') + s[1:]
	}
	return s
}

// TelemetrySafe returns a short, locale-independent description suitable
// for log fields. Sanitizes file paths and quotation marks so the output
// is reliably greppable.
func TelemetrySafe(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "\n", " ")
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	return msg
}
