/*
 * ChatCLI - Coder turn-UI raw key decoder
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"unicode/utf8"
)

// Once the TTY is in raw mode every byte that the user types arrives
// at our reader instead of being processed by the kernel line
// discipline. The decoder in this file converts a stream of raw bytes
// into Key events the input loop knows how to act on. Keeping it pure
// (no IO, no terminal state) is what makes the loop testable without
// a real pseudo-terminal — the tests for backspace, Enter, and Ctrl+C
// all run against fixed byte slices.

// KeyKind enumerates the input categories the mini-readline cares
// about. Anything not in this list (escape sequences for arrow keys,
// function keys, mouse events) is reported as KeyUnknown so the loop
// can drop it silently — Phase B intentionally does NOT support
// arrows or history navigation; that is reserved for a later phase
// once the basic split UI is proven to work.
type KeyKind int

const (
	// KeyChar is a printable Unicode codepoint that should land in
	// the input buffer and be echoed at the cursor's current column.
	KeyChar KeyKind = iota

	// KeyEnter is CR (\r) or LF (\n). The line discipline normally
	// translates one into the other; in raw mode the terminal
	// sends whichever the user's keyboard produces (usually CR on
	// macOS, LF on Linux). We accept both.
	KeyEnter

	// KeyBackspace is BS (\b, 0x08) or DEL (0x7f). macOS sends DEL
	// for the Delete key by default, Linux usually sends BS. Both
	// mean "erase the previous character".
	KeyBackspace

	// KeyCtrlC is ETX (0x03). In raw mode the kernel does NOT
	// translate this into SIGINT — the loop is responsible for
	// surfacing it as a cancel/abort signal to the caller.
	KeyCtrlC

	// KeyCtrlD is EOT (0x04). Empty buffer + Ctrl+D = "end of
	// input" (request shutdown of the input loop). With a non-empty
	// buffer the loop ignores it to match readline conventions.
	KeyCtrlD

	// KeyCtrlU is NAK (0x15) — "kill line": discard everything
	// the user has typed since the last Enter. The loop redraws
	// the input row to reflect the empty buffer.
	KeyCtrlU

	// KeyCtrlW is ETB (0x17) — "kill word": delete the previous
	// whitespace-delimited word. Convenient enough to be table
	// stakes for any line editor; the implementation lives in the
	// loop because the buffer is the source of truth for "what's
	// the previous word".
	KeyCtrlW

	// KeyCtrlA is SOH (0x01) — move cursor to beginning of line.
	// Readline / bash muscle memory.
	KeyCtrlA

	// KeyCtrlE is ENQ (0x05) — move cursor to end of line.
	// Companion to KeyCtrlA.
	KeyCtrlE

	// KeyArrowLeft / Right move the cursor one rune within the
	// buffer. Decoded from the standard CSI A/B/C/D sequences plus
	// the modern application-mode SS3 variants some terminals
	// emit. Up/Down are reserved for history navigation.
	KeyArrowLeft
	KeyArrowRight
	KeyArrowUp
	KeyArrowDown

	// KeyHome / KeyEnd are the dedicated keys on most keyboards.
	// Distinct from Ctrl+A / Ctrl+E because some users have one
	// and not the other; supporting both costs almost nothing.
	KeyHome
	KeyEnd

	// KeyDelete is forward delete (the "Delete" key, not Backspace).
	// Decoded from CSI 3 ~ which is the canonical sequence on
	// every terminal that has the key.
	KeyDelete

	// KeyUnknown covers escape sequences (function keys, mouse
	// events) and anything else the loop does not handle. The loop
	// discards it so the user's terminal does not see a stray
	// escape sequence appear inline.
	KeyUnknown
)

// Key is one decoded input event. For KeyChar the Rune field holds
// the codepoint; for everything else it is the zero value (0).
type Key struct {
	Kind KeyKind
	Rune rune
}

// DecodeOne reads one key event from the start of buf and returns the
// event plus the number of bytes consumed. When buf does not contain
// a complete event (e.g. a multi-byte UTF-8 sequence with only the
// leading byte present) it returns Key{Kind:KeyUnknown}, 0 — the
// caller should accumulate more bytes and try again.
//
// The function deliberately does NOT consume escape sequences (CSI,
// SS3) byte-by-byte: it recognizes the lead ESC (0x1b) and skips
// ahead based on the well-known short forms. A future Phase B+ may
// extend this to interpret arrow keys; for now they all map to
// KeyUnknown via the "discard up to N bytes" fast path so they don't
// leak into the input buffer as garbage glyphs.
func DecodeOne(buf []byte) (Key, int) {
	if len(buf) == 0 {
		return Key{Kind: KeyUnknown}, 0
	}

	b := buf[0]

	switch b {
	case 0x01:
		return Key{Kind: KeyCtrlA}, 1
	case 0x03:
		return Key{Kind: KeyCtrlC}, 1
	case 0x04:
		return Key{Kind: KeyCtrlD}, 1
	case 0x05:
		return Key{Kind: KeyCtrlE}, 1
	case 0x08, 0x7f:
		return Key{Kind: KeyBackspace}, 1
	case 0x0a, 0x0d:
		// CR + LF arriving as one chunk (rare in raw mode but
		// possible on terminals that buffer keystrokes) consumes
		// both bytes as a single Enter so the next DecodeOne does
		// not see the orphan LF and emit a second Enter.
		if b == 0x0d && len(buf) >= 2 && buf[1] == 0x0a {
			return Key{Kind: KeyEnter}, 2
		}
		return Key{Kind: KeyEnter}, 1
	case 0x15:
		return Key{Kind: KeyCtrlU}, 1
	case 0x17:
		return Key{Kind: KeyCtrlW}, 1
	case 0x1b:
		// ESC: we don't interpret arrow keys / function keys yet.
		// Skip the well-known short forms so they do not appear
		// inline. The lengths below cover the common CSI / SS3
		// sequences (ESC [ A for up, ESC O P for F1, etc.). If
		// the buffer is too short to contain a full sequence we
		// return 0 consumed and wait for more bytes.
		return decodeEscape(buf)
	}

	if b < 0x20 {
		// Other control bytes (tab, etc.) are not yet supported.
		// Drop them silently so they don't show up as garbage.
		return Key{Kind: KeyUnknown}, 1
	}

	// Printable byte (or start of a UTF-8 sequence). FullRune
	// distinguishes "incomplete sequence, wait for more" from
	// "invalid byte, drop it" — DecodeRune alone cannot, because it
	// returns (RuneError, 1) in both cases. Without this branch a
	// multi-byte rune split across two reads would be silently
	// corrupted into a replacement char.
	if !utf8.FullRune(buf) {
		return Key{Kind: KeyUnknown}, 0
	}
	r, size := utf8.DecodeRune(buf)
	if r == utf8.RuneError && size == 1 {
		// Invalid byte — consume and drop.
		return Key{Kind: KeyUnknown}, 1
	}
	return Key{Kind: KeyChar, Rune: r}, size
}

// decodeEscape consumes the bytes belonging to an escape sequence
// starting with ESC and identifies arrows, Home, End, Delete, and
// other navigation keys. Unknown sequences are consumed (so they
// don't leak into the buffer as garbage) and reported as KeyUnknown.
//
// Recognized sequences:
//
//	ESC [ A / B / C / D    arrow Up / Down / Right / Left (CSI)
//	ESC [ H / F            Home / End (CSI, xterm convention)
//	ESC [ 1 ~ / 7 ~        Home (alternate forms)
//	ESC [ 4 ~ / 8 ~        End (alternate forms)
//	ESC [ 3 ~              Delete (forward delete)
//	ESC O A / B / C / D    arrows (SS3, application mode)
//	ESC O H / F            Home / End (SS3)
//
// CSI variants with modifiers (e.g. ESC [ 1 ; 5 D for Ctrl+Left)
// are consumed as KeyUnknown — the buffer doesn't need them yet, but
// they MUST be swallowed so the modifier prefix doesn't end up
// echoed as literal text.
//
// Anything longer (mouse events, OSC, DCS) falls into a generous
// catch-all of "skip until a final byte in 0x40..0x7e" capped at 16
// bytes so a malformed stream cannot starve the loop.
func decodeEscape(buf []byte) (Key, int) {
	if len(buf) == 1 {
		// Bare ESC: treat as Unknown and consume the single byte.
		// (Phase B does not bind anything to bare Esc; the rewind
		// gesture in chat mode uses double-Esc, which is captured
		// by go-prompt, not by this decoder.)
		return Key{Kind: KeyUnknown}, 1
	}

	switch buf[1] {
	case '[': // CSI
		if len(buf) < 3 {
			return Key{Kind: KeyUnknown}, 0
		}
		// Short forms: ESC [ X where X is the final byte.
		switch buf[2] {
		case 'A':
			return Key{Kind: KeyArrowUp}, 3
		case 'B':
			return Key{Kind: KeyArrowDown}, 3
		case 'C':
			return Key{Kind: KeyArrowRight}, 3
		case 'D':
			return Key{Kind: KeyArrowLeft}, 3
		case 'H':
			return Key{Kind: KeyHome}, 3
		case 'F':
			return Key{Kind: KeyEnd}, 3
		}
		// Longer forms: ESC [ <param(s)> <final>. We hand-decode
		// the few we care about and fall back to "swallow up to
		// the final byte" for the rest.
		if len(buf) >= 4 && buf[3] == '~' {
			switch buf[2] {
			case '1', '7':
				return Key{Kind: KeyHome}, 4
			case '3':
				return Key{Kind: KeyDelete}, 4
			case '4', '8':
				return Key{Kind: KeyEnd}, 4
			}
		}
		// Generic CSI consume: scan to a final byte (0x40..0x7e)
		// up to 16 bytes. Covers modified arrows (ESC [ 1;5 D),
		// SGR mouse reports, etc. — all dropped as KeyUnknown.
		end := 2
		for end < len(buf) && end < 16 {
			if buf[end] >= 0x40 && buf[end] <= 0x7e {
				return Key{Kind: KeyUnknown}, end + 1
			}
			end++
		}
		return Key{Kind: KeyUnknown}, 0
	case 'O': // SS3 (application mode)
		if len(buf) < 3 {
			return Key{Kind: KeyUnknown}, 0
		}
		switch buf[2] {
		case 'A':
			return Key{Kind: KeyArrowUp}, 3
		case 'B':
			return Key{Kind: KeyArrowDown}, 3
		case 'C':
			return Key{Kind: KeyArrowRight}, 3
		case 'D':
			return Key{Kind: KeyArrowLeft}, 3
		case 'H':
			return Key{Kind: KeyHome}, 3
		case 'F':
			return Key{Kind: KeyEnd}, 3
		}
		return Key{Kind: KeyUnknown}, 3
	default:
		// Unknown short escape: consume ESC + one byte.
		return Key{Kind: KeyUnknown}, 2
	}
}
