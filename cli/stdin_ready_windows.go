//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

var (
	procPeekConsoleInputW = kernel32.NewProc("PeekConsoleInputW")
	procReadConsoleInputW = kernel32.NewProc("ReadConsoleInputW")
)

// stdinPollReady waits up to timeout for stdin to have TEXT input available.
// On Windows this uses WaitForSingleObject on the console input handle plus a
// PeekConsoleInput classification pass.
//
// WaitForSingleObject alone is not enough: a console input handle is signaled
// by ANY pending event — window resize, focus changes, mouse movement — not
// just keystrokes. Treating those as "ready" sends the caller into a blocking
// os.Stdin.Read that only returns when the user actually types, and
// stopStdinReader cannot cancel that read (the root cause of the coder-mode
// "typing shows nothing until Enter" hang). So after the wait we peek the
// queue: only a key-down event carrying a character counts as ready, and the
// non-text records in front of it are consumed so they stop re-signaling the
// wait on every poll cycle.
func stdinPollReady(timeout time.Duration) bool {
	h := syscall.Handle(os.Stdin.Fd())
	r, _ := syscall.WaitForSingleObject(h, uint32(timeout.Milliseconds()))
	if r != syscall.WAIT_OBJECT_0 {
		return false
	}
	return consoleHasPendingText(h)
}

// consoleHasPendingText reports whether the console input queue holds a
// key-down event with a character, discarding the non-text events (resize,
// focus, mouse, key-up) queued in front of it. The reader goroutine is the
// only console consumer while it runs, so dropping those records is safe.
func consoleHasPendingText(h syscall.Handle) bool {
	const maxRecords = 64
	var recs [maxRecords]inputRecord
	var n uint32

	// #nosec G103 -- the kernel fills the record array before the synchronous
	// syscall returns; recs outlives the call.
	r1, _, _ := procPeekConsoleInputW.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&recs[0])),
		maxRecords,
		uintptr(unsafe.Pointer(&n)),
	)
	if r1 == 0 {
		// Peek unavailable (e.g. stdin is a pipe, not a console). Fall back
		// to the pre-classification behavior: assume the wait meant data.
		return true
	}
	if n == 0 {
		return false
	}

	textAt := -1
	for i := 0; i < int(n); i++ {
		if recs[i].EventType == keyEventType && recs[i].Event.KeyDown == 1 && recs[i].Event.UnicodeChar != 0 {
			textAt = i
			break
		}
	}

	discard := int(n)
	if textAt >= 0 {
		discard = textAt
	}
	if discard > 0 {
		var read uint32
		// #nosec G103 -- same contract as the peek above.
		_, _, _ = procReadConsoleInputW.Call(
			uintptr(h),
			uintptr(unsafe.Pointer(&recs[0])),
			uintptr(discard),
			uintptr(unsafe.Pointer(&read)),
		)
	}
	return textAt >= 0
}
