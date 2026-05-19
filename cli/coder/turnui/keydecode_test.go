/*
 * ChatCLI - Coder turn-UI key decoder tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDecodeOne_ControlBytes walks every control byte the mini-
// readline acts on. The byte values are non-obvious (0x7f for
// Backspace on macOS, 0x08 elsewhere) so locking them in a table
// makes the contract reviewable at a glance — and catches regressions
// where a future cleanup pass swaps one for the other and silently
// breaks Backspace on half the user base.
func TestDecodeOne_ControlBytes(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want KeyKind
		size int
	}{
		{"Ctrl+C (ETX)", []byte{0x03}, KeyCtrlC, 1},
		{"Ctrl+D (EOT)", []byte{0x04}, KeyCtrlD, 1},
		{"Backspace (BS)", []byte{0x08}, KeyBackspace, 1},
		{"Backspace (DEL, macOS)", []byte{0x7f}, KeyBackspace, 1},
		{"Enter (CR)", []byte{0x0d}, KeyEnter, 1},
		{"Enter (LF)", []byte{0x0a}, KeyEnter, 1},
		{"CRLF collapses to one Enter", []byte{0x0d, 0x0a}, KeyEnter, 2},
		{"Ctrl+U (NAK, kill line)", []byte{0x15}, KeyCtrlU, 1},
		{"Ctrl+W (ETB, kill word)", []byte{0x17}, KeyCtrlW, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, n := DecodeOne(tc.in)
			assert.Equal(t, tc.want, k.Kind)
			assert.Equal(t, tc.size, n)
		})
	}
}

// TestDecodeOne_PrintableASCII confirms the common case: a plain
// letter byte produces KeyChar with the right rune and consumes
// exactly one byte. If this regresses every keystroke turns into a
// no-op and the user cannot type at all — the test is small but
// load-bearing.
func TestDecodeOne_PrintableASCII(t *testing.T) {
	k, n := DecodeOne([]byte("a"))
	assert.Equal(t, KeyChar, k.Kind)
	assert.Equal(t, rune('a'), k.Rune)
	assert.Equal(t, 1, n)
}

// TestDecodeOne_MultiByteUTF8 covers the case the user actually
// hits — typing accented characters in Portuguese ("ç", "ã") and
// emoji. The decoder must consume the full UTF-8 sequence and emit
// a single KeyChar, not one KeyChar per byte (which would write
// mojibake to the buffer).
func TestDecodeOne_MultiByteUTF8(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want rune
		size int
	}{
		{"c-cedilla", []byte("ç"), 'ç', 2},
		{"a-tilde", []byte("ã"), 'ã', 2},
		{"euro sign", []byte("€"), '€', 3},
		{"face-with-tears-of-joy emoji", []byte("😂"), '😂', 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, n := DecodeOne(tc.in)
			assert.Equal(t, KeyChar, k.Kind)
			assert.Equal(t, tc.want, k.Rune)
			assert.Equal(t, tc.size, n)
		})
	}
}

// TestDecodeOne_PartialUTF8WaitsForMore is the boundary case: when
// only the leading byte of a multi-byte rune is in the buffer, the
// decoder must signal "need more" by returning 0 consumed. Returning
// 1 (consume the lead byte and drop it) would lose the character;
// returning a fake KeyChar would inject mojibake.
func TestDecodeOne_PartialUTF8WaitsForMore(t *testing.T) {
	// 0xC3 is the lead byte of "ç" (0xC3 0xA7) — alone, it is
	// incomplete and should be held until the trailing byte arrives.
	k, n := DecodeOne([]byte{0xc3})
	assert.Equal(t, KeyUnknown, k.Kind)
	assert.Equal(t, 0, n, "incomplete UTF-8 must signal 'need more bytes'")
}

// TestDecodeOne_InvalidUTF8ByteIsDropped guards against a bad byte
// hanging the decoder in an infinite loop. The byte is consumed (n=1)
// and reported as KeyUnknown so the caller advances past it.
func TestDecodeOne_InvalidUTF8ByteIsDropped(t *testing.T) {
	k, n := DecodeOne([]byte{0xff})
	assert.Equal(t, KeyUnknown, k.Kind)
	assert.Equal(t, 1, n)
}

// TestDecodeOne_EscapeSequencesAreSwallowed makes sure arrow keys
// and other escape sequences do not leak into the input buffer as
// literal "[A" / "[B" garbage. Phase B does not act on them, but it
// MUST consume them — Phase A's tests asserted nothing about input,
// so this is the first chance to lock the swallow-don't-leak contract.
func TestDecodeOne_EscapeSequencesAreSwallowed(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		size int
	}{
		{"up arrow CSI A", []byte{0x1b, '[', 'A'}, 3},
		{"down arrow CSI B", []byte{0x1b, '[', 'B'}, 3},
		{"F1 SS3 P", []byte{0x1b, 'O', 'P'}, 3},
		{"page-up CSI 5 ~", []byte{0x1b, '[', '5', '~'}, 4},
		{"bare Esc", []byte{0x1b}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k, n := DecodeOne(tc.in)
			assert.Equal(t, KeyUnknown, k.Kind, "escape sequence must NOT surface as a Char")
			assert.Equal(t, tc.size, n)
		})
	}
}

// TestDecodeOne_PartialCSIWaitsForMore mirrors the partial-UTF-8 case
// for escape sequences: when only ESC [ is in the buffer the decoder
// returns 0 so the caller waits for the terminating byte. Returning
// a fake length here would consume bytes from the next event and
// corrupt the stream.
func TestDecodeOne_PartialCSIWaitsForMore(t *testing.T) {
	k, n := DecodeOne([]byte{0x1b, '['})
	assert.Equal(t, KeyUnknown, k.Kind)
	assert.Equal(t, 0, n)
}

// TestDecodeOne_EmptyBufferReturnsZero is the null case: nothing in,
// nothing out. The loop calls DecodeOne in a tight read-decode cycle;
// an empty buffer must not loop forever.
func TestDecodeOne_EmptyBufferReturnsZero(t *testing.T) {
	k, n := DecodeOne(nil)
	assert.Equal(t, KeyUnknown, k.Kind)
	assert.Equal(t, 0, n)
}
