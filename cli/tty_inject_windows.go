//go:build windows

/*
 * tty_inject_windows.go — Windows console input injection.
 *
 * Windows has no TIOCSTI; the equivalent mechanism is the WriteConsoleInputW
 * Win32 API, which writes pre-cooked INPUT_RECORD structures (key events,
 * mouse events, etc.) into a console's input buffer. From the application's
 * read side, the events are indistinguishable from real keystrokes — exactly
 * what we need to wake go-prompt out of its blocking ReadConsoleInput call
 * for the park auto-resume flow.
 *
 * For each character in the line:
 *
 *   1. INPUT_RECORD with EventType=KEY_EVENT, KeyDown=true, UnicodeChar=c
 *   2. INPUT_RECORD with EventType=KEY_EVENT, KeyDown=false, UnicodeChar=c
 *
 * For the trailing '\r' we set VirtualKeyCode=VK_RETURN so the read side
 * sees a proper Enter event (matches what the keyboard's Enter key emits).
 *
 * This is the dual of the macOS/Linux TIOCSTI path. The key difference is
 * that Windows uses event records (richer than raw bytes) so we don't need
 * the "split body + sleep + \r" workaround the Unix path needs against
 * go-prompt's bytes.Equal-based key parser. Each event is delivered as a
 * single ReadConsoleInput call return, which go-prompt's Windows reader
 * dispatches per-key. Submission lands naturally on the VK_RETURN event.
 */
package cli

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// errTTYInjectUnsupported is the platform-agnostic sentinel callers
// inspect when injection is rejected. Even though Windows now supports
// a real injection path, individual sessions can fail (e.g. when
// chatcli is invoked without an attached console — piped under a CI
// runner). The bridge still prefers to log + fall back gracefully.
var errTTYInjectUnsupported = errors.New("tty inject: WriteConsoleInputW not available in this session")

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procWriteConsoleInputW  = kernel32.NewProc("WriteConsoleInputW")
	procGetStdHandle        = kernel32.NewProc("GetStdHandle")
)

const (
	stdInputHandle = ^uintptr(0) - 9 // STD_INPUT_HANDLE = -10
	keyEventType   = 0x0001          // KEY_EVENT
	vkReturn       = 0x0D            // VK_RETURN
)

// keyEventRecord mirrors the Win32 KEY_EVENT_RECORD struct used inside
// INPUT_RECORD's union when EventType == KEY_EVENT. Field order and
// sizes match the C ABI exactly so the syscall passes correct bytes.
type keyEventRecord struct {
	KeyDown         int32  // BOOL — 1 if key was pressed, 0 if released
	RepeatCount     uint16 // typically 1 for synthesized input
	VirtualKeyCode  uint16 // VK_* constant; 0 for "any key, use UnicodeChar"
	VirtualScanCode uint16 // hardware scan code; 0 for synthesized input
	UnicodeChar     uint16 // UTF-16 code unit (wchar_t)
	ControlKeyState uint32 // SHIFT/CTRL/ALT modifier flags; 0 for plain
}

// inputRecord mirrors the Win32 INPUT_RECORD struct. The Event field is
// a union in C; we declare it as the maximum-sized variant (KEY_EVENT)
// because that is the only type we ever emit. Padding bytes after
// EventType match what the compiler inserts to align Event on a 4-byte
// boundary in the C ABI.
type inputRecord struct {
	EventType uint16
	_         [2]byte // padding for ABI alignment
	Event     keyEventRecord
}

// injectTTYLine writes line + Enter to the controlling console's input
// buffer using WriteConsoleInputW. Each character becomes a key-down +
// key-up event pair. The trailing event is a synthesized VK_RETURN so
// go-prompt's reader treats it as a real Enter press (not just a \r
// character) and the prompt submits naturally.
func injectTTYLine(line string) error {
	if line == "" {
		return nil
	}

	hStdin, _, _ := procGetStdHandle.Call(stdInputHandle)
	// GetStdHandle returns INVALID_HANDLE_VALUE (-1) on failure or
	// 0 when no handle is attached (e.g. detached process). Both are
	// non-fatal — we log via the bridge and rely on the executor-hook
	// drain instead.
	if hStdin == 0 || hStdin == ^uintptr(0) {
		return errTTYInjectUnsupported
	}

	// UTF-16 encode the line so multibyte characters survive the round
	// trip. ASCII passes through unchanged.
	utf16Line := syscall.StringToUTF16(line)
	// Drop the trailing nul terminator — it would inject as a literal
	// 0-byte event the line editor stores as a control char.
	if n := len(utf16Line); n > 0 && utf16Line[n-1] == 0 {
		utf16Line = utf16Line[:n-1]
	}

	// Build the event stream: each char gets two events (down + up),
	// plus a final VK_RETURN pair to submit the line.
	events := make([]inputRecord, 0, 2*(len(utf16Line)+1))
	for _, c := range utf16Line {
		events = append(events,
			inputRecord{
				EventType: keyEventType,
				Event: keyEventRecord{
					KeyDown:     1,
					RepeatCount: 1,
					UnicodeChar: c,
				},
			},
			inputRecord{
				EventType: keyEventType,
				Event: keyEventRecord{
					KeyDown:     0,
					RepeatCount: 1,
					UnicodeChar: c,
				},
			},
		)
	}
	// Trailing Enter — VirtualKeyCode=VK_RETURN, UnicodeChar='\r'. The
	// VK_RETURN flag is what makes go-prompt's Windows input reader
	// classify the event as an Enter key, triggering the submit path.
	events = append(events,
		inputRecord{
			EventType: keyEventType,
			Event: keyEventRecord{
				KeyDown:        1,
				RepeatCount:    1,
				VirtualKeyCode: vkReturn,
				UnicodeChar:    '\r',
			},
		},
		inputRecord{
			EventType: keyEventType,
			Event: keyEventRecord{
				KeyDown:        0,
				RepeatCount:    1,
				VirtualKeyCode: vkReturn,
				UnicodeChar:    '\r',
			},
		},
	)

	var written uint32
	// #nosec G103 -- WriteConsoleInputW signature requires a pointer
	// to the events array; events is a stack/heap-allocated Go slice
	// whose lifetime spans the synchronous syscall. The kernel copies
	// each record before returning. No GC interaction.
	r1, _, callErr := procWriteConsoleInputW.Call(
		hStdin,
		uintptr(unsafe.Pointer(&events[0])),
		uintptr(len(events)),
		uintptr(unsafe.Pointer(&written)),
	)
	if r1 == 0 {
		return fmt.Errorf("WriteConsoleInputW failed: %v", callErr)
	}
	if written != uint32(len(events)) {
		return fmt.Errorf("WriteConsoleInputW: short write (%d/%d events)", written, len(events))
	}
	return nil
}
