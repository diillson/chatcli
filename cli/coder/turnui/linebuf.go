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
// Phase G adds a cursor position so arrow keys can navigate within
// the buffer; inserts and deletes now happen at the cursor rather
// than always at the end. The cursor is measured in runes from the
// start; valid range is [0, len(runes)]. cursor == len(runes) means
// "after the last rune" (end-of-line position).
//
// The buffer is a pure data structure: no IO, no escape sequences.
// The input loop wraps it with the terminal writes that actually
// repaint the input row when the buffer changes. Keeping the two
// layers separate is what lets every Backspace / Ctrl+U / Ctrl+W /
// arrow-key behavior have a unit test that runs without a TTY.
type LineBuffer struct {
	// runes is the in-memory representation. We keep runes instead
	// of a string so insertions / deletions are O(1) at the end
	// and there is no temptation to do byte-level slicing that
	// would split a multi-byte glyph.
	runes []rune

	// cursor is the insertion point in rune units, NOT bytes.
	// Range: [0, len(runes)]. When cursor == len(runes) the next
	// Insert appends; when cursor == 0 Backspace is a no-op and
	// Delete (forward delete) removes the first rune.
	cursor int
}

// NewLineBuffer returns an empty buffer ready to accept input. The
// cursor starts at position 0 (which equals len, since the buffer is
// empty).
func NewLineBuffer() *LineBuffer {
	return &LineBuffer{runes: make([]rune, 0, 64), cursor: 0}
}

// Cursor returns the current cursor position in rune units from the
// start of the buffer. Used by PaintInput to compute the absolute
// column on the input row.
func (b *LineBuffer) Cursor() int { return b.cursor }

// Append adds a rune to the end of the buffer and parks the cursor
// after it. Retained for compatibility with the Phase B test suite
// (and for callers that bypass the cursor-aware Insert path); new
// code should prefer Insert for clarity.
func (b *LineBuffer) Append(r rune) {
	b.runes = append(b.runes, r)
	b.cursor = len(b.runes)
}

// Insert places r at the current cursor position and advances the
// cursor by one. Equivalent to Append when the cursor is at end-of-
// line; the distinction matters only for arrow-key-navigated edits.
func (b *LineBuffer) Insert(r rune) {
	if b.cursor >= len(b.runes) {
		b.Append(r)
		return
	}
	b.runes = append(b.runes, 0)
	copy(b.runes[b.cursor+1:], b.runes[b.cursor:])
	b.runes[b.cursor] = r
	b.cursor++
}

// Backspace removes the rune immediately before the cursor and moves
// the cursor back one. Returns true when something was removed so
// the caller can decide whether to repaint the input row (a no-op
// backspace at the start of the line should not cost a redraw).
func (b *LineBuffer) Backspace() bool {
	if b.cursor == 0 || len(b.runes) == 0 {
		return false
	}
	if b.cursor >= len(b.runes) {
		// Fast path: cursor at end, drop trailing rune.
		b.runes = b.runes[:len(b.runes)-1]
		b.cursor = len(b.runes)
		return true
	}
	b.runes = append(b.runes[:b.cursor-1], b.runes[b.cursor:]...)
	b.cursor--
	return true
}

// Delete removes the rune at the current cursor position (forward
// delete; the cursor stays put because the next rune slid into its
// place). No-op at end-of-line. Used by the future Delete key.
func (b *LineBuffer) Delete() bool {
	if b.cursor >= len(b.runes) {
		return false
	}
	b.runes = append(b.runes[:b.cursor], b.runes[b.cursor+1:]...)
	return true
}

// MoveLeft moves the cursor one rune to the left. Returns true on
// movement so the caller knows whether to repaint the input row.
func (b *LineBuffer) MoveLeft() bool {
	if b.cursor == 0 {
		return false
	}
	b.cursor--
	return true
}

// MoveRight moves the cursor one rune to the right. Returns true on
// movement.
func (b *LineBuffer) MoveRight() bool {
	if b.cursor >= len(b.runes) {
		return false
	}
	b.cursor++
	return true
}

// MoveStart parks the cursor at column 0 (Ctrl+A / Home semantics).
// Returns true when the cursor moved.
func (b *LineBuffer) MoveStart() bool {
	if b.cursor == 0 {
		return false
	}
	b.cursor = 0
	return true
}

// MoveEnd parks the cursor after the last rune (Ctrl+E / End
// semantics). Returns true when the cursor moved.
func (b *LineBuffer) MoveEnd() bool {
	if b.cursor == len(b.runes) {
		return false
	}
	b.cursor = len(b.runes)
	return true
}

// SetContents replaces the buffer with the given string and parks the
// cursor at the end. Used by the history navigation path to swap in
// a recalled line; PaintInput will repaint with the new contents.
func (b *LineBuffer) SetContents(s string) {
	b.runes = b.runes[:0]
	for _, r := range s {
		b.runes = append(b.runes, r)
	}
	b.cursor = len(b.runes)
}

// KillLine clears the entire buffer (Ctrl+U semantics). Returns true
// when the buffer was non-empty so the caller can skip a no-op
// redraw on a fresh prompt. Cursor is reset to position 0.
func (b *LineBuffer) KillLine() bool {
	if len(b.runes) == 0 {
		return false
	}
	b.runes = b.runes[:0]
	b.cursor = 0
	return true
}

// KillWord deletes the word immediately before the cursor (Ctrl+W
// semantics). Whitespace before the cursor is consumed first, then
// runes are removed until the next whitespace boundary or start of
// line. Mirrors readline's behavior so users with muscle memory from
// bash see the same effect. Returns true when something changed.
func (b *LineBuffer) KillWord() bool {
	if b.cursor == 0 {
		return false
	}
	end := b.cursor

	// Step 1: chew off whitespace immediately before the cursor.
	for b.cursor > 0 && unicode.IsSpace(b.runes[b.cursor-1]) {
		b.cursor--
	}
	// Step 2: chew off the word itself, up to the next whitespace.
	for b.cursor > 0 && !unicode.IsSpace(b.runes[b.cursor-1]) {
		b.cursor--
	}
	if b.cursor == end {
		return false
	}
	b.runes = append(b.runes[:b.cursor], b.runes[end:]...)
	return true
}

// String returns the buffer as a string for submission when the user
// presses Enter. The result is a copy; mutating the buffer afterwards
// does not affect a previously returned string.
func (b *LineBuffer) String() string {
	return string(b.runes)
}

// Reset empties the buffer without reallocating the underlying slice.
// Used after Enter so the same buffer can serve the next input cycle
// without churning the allocator. Cursor is reset to position 0.
func (b *LineBuffer) Reset() {
	b.runes = b.runes[:0]
	b.cursor = 0
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
