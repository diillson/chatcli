/*
 * ChatCLI - Coder turn-UI line buffer
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"strings"
	"unicode"
)

// LineBuffer is the mutable text the mini-readline shows in the input
// row and submits when the user presses Enter. It is intentionally
// rune-aware: every visible character — including multi-byte
// codepoints like "ç" or emoji — is one logical unit, so Backspace
// deletes one glyph, not one byte. The visible column count drives
// cursor positioning on the input row.
//
// The buffer is a pure data structure: no IO, no escape sequences.
// The input loop wraps it with the terminal writes that actually
// repaint the input row when the buffer changes. Keeping the two
// layers separate is what lets every Backspace / Ctrl+U / Ctrl+W
// behavior have a unit test that runs without a TTY.
type LineBuffer struct {
	// runes is the in-memory representation. We keep runes instead
	// of a string so insertions / deletions are O(1) at the end
	// and there is no temptation to do byte-level slicing that
	// would split a multi-byte glyph.
	runes []rune
}

// NewLineBuffer returns an empty buffer ready to accept input.
func NewLineBuffer() *LineBuffer {
	return &LineBuffer{runes: make([]rune, 0, 64)}
}

// Append adds a rune to the end of the buffer. The mini-readline only
// supports appending at the cursor (which is always at the end of the
// buffer for Phase B — arrow-key navigation is reserved for a future
// phase), so an Append-only API matches the actual capabilities.
func (b *LineBuffer) Append(r rune) {
	b.runes = append(b.runes, r)
}

// Backspace removes the trailing rune, if any. Returns true when
// something was removed so the caller can decide whether to repaint
// the input row (a no-op backspace at the start of the line should
// not cost a redraw).
func (b *LineBuffer) Backspace() bool {
	if len(b.runes) == 0 {
		return false
	}
	b.runes = b.runes[:len(b.runes)-1]
	return true
}

// KillLine clears the entire buffer (Ctrl+U semantics). Returns true
// when the buffer was non-empty so the caller can skip a no-op
// redraw on a fresh prompt.
func (b *LineBuffer) KillLine() bool {
	if len(b.runes) == 0 {
		return false
	}
	b.runes = b.runes[:0]
	return true
}

// KillWord deletes the trailing whitespace-delimited word (Ctrl+W
// semantics). Trailing whitespace is consumed first, then runes are
// removed until the next whitespace boundary. Mirrors readline's
// behavior so users with muscle memory from bash see the same effect.
// Returns true when something changed.
func (b *LineBuffer) KillWord() bool {
	if len(b.runes) == 0 {
		return false
	}
	original := len(b.runes)

	// Step 1: chew off trailing whitespace.
	for len(b.runes) > 0 && unicode.IsSpace(b.runes[len(b.runes)-1]) {
		b.runes = b.runes[:len(b.runes)-1]
	}
	// Step 2: chew off the word itself, up to the next whitespace.
	for len(b.runes) > 0 && !unicode.IsSpace(b.runes[len(b.runes)-1]) {
		b.runes = b.runes[:len(b.runes)-1]
	}

	return len(b.runes) != original
}

// String returns the buffer as a string for submission when the user
// presses Enter. The result is a copy; mutating the buffer afterwards
// does not affect a previously returned string.
func (b *LineBuffer) String() string {
	return string(b.runes)
}

// Reset empties the buffer without reallocating the underlying slice.
// Used after Enter so the same buffer can serve the next input cycle
// without churning the allocator.
func (b *LineBuffer) Reset() {
	b.runes = b.runes[:0]
}

// VisibleWidth approximates the column count needed to render the
// buffer's current contents. Used by the input loop to compute the
// echo column on the input row. The approximation is conservative —
// we treat every rune as one column, which is correct for ASCII and
// most Latin-1 input but underestimates East Asian wide glyphs. A
// future phase that supports wide glyphs should plumb runewidth here.
func (b *LineBuffer) VisibleWidth() int {
	return len(b.runes)
}

// Trim returns the buffer's contents with leading/trailing whitespace
// removed. The mini-readline submits the trimmed form because the
// agent loop's history append expects "user typed this" rather than
// "user typed this and a stray space".
func (b *LineBuffer) Trim() string {
	return strings.TrimSpace(string(b.runes))
}
