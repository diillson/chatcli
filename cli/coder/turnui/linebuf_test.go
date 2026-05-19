/*
 * ChatCLI - Coder turn-UI line buffer tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestLineBuffer_AppendString builds a typical word and checks the
// rendered string. The append-rune-by-rune call pattern matches the
// reader loop, so a regression in Append would surface here before
// reaching the live terminal.
func TestLineBuffer_AppendString(t *testing.T) {
	b := NewLineBuffer()
	for _, r := range "hello" {
		b.Append(r)
	}
	assert.Equal(t, "hello", b.String())
	assert.Equal(t, 5, b.VisibleWidth())
}

// TestLineBuffer_BackspaceDeletesOneGlyph is the critical invariant:
// Backspace must remove a whole rune even when that rune is encoded
// as multiple bytes. Without it, deleting "ç" (2 bytes in UTF-8) would
// leave a dangling 0xC3 lead byte in the buffer that would render as
// the replacement character on the next paint.
func TestLineBuffer_BackspaceDeletesOneGlyph(t *testing.T) {
	b := NewLineBuffer()
	for _, r := range "olá" {
		b.Append(r)
	}
	assert.Equal(t, "olá", b.String())

	changed := b.Backspace()
	assert.True(t, changed)
	assert.Equal(t, "ol", b.String(), "must delete the full 'á' codepoint, not a single byte")
}

// TestLineBuffer_BackspaceOnEmpty proves the no-op contract. The
// caller relies on the false return to skip a useless redraw of the
// input row when the user hits Backspace at the start of the line.
func TestLineBuffer_BackspaceOnEmpty(t *testing.T) {
	b := NewLineBuffer()
	changed := b.Backspace()
	assert.False(t, changed)
	assert.Equal(t, "", b.String())
}

// TestLineBuffer_KillLine clears everything (Ctrl+U). Mirrors bash /
// readline so the muscle memory transfers.
func TestLineBuffer_KillLine(t *testing.T) {
	b := NewLineBuffer()
	for _, r := range "the quick brown fox" {
		b.Append(r)
	}
	assert.True(t, b.KillLine())
	assert.Equal(t, "", b.String())
	assert.False(t, b.KillLine(), "second KillLine on empty is a no-op")
}

// TestLineBuffer_KillWord exercises the two-phase deletion: trailing
// whitespace first, then the word. The cases below are the ones that
// surprise people: a single trailing space, multiple trailing spaces,
// and a word that ends without whitespace at end-of-buffer.
func TestLineBuffer_KillWord(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"word at end of line", "the quick brown fox", "the quick brown "},
		{"single trailing space", "hello ", ""},
		{"multiple trailing spaces", "hello   ", ""},
		{"empty buffer", "", ""},
		{"single word", "fox", ""},
		{"trailing word with mixed whitespace", "hello\tworld", "hello\t"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := NewLineBuffer()
			for _, r := range tc.in {
				b.Append(r)
			}
			b.KillWord()
			assert.Equal(t, tc.want, b.String())
		})
	}
}

// TestLineBuffer_ResetKeepsCapacity verifies the buffer can serve
// successive input cycles without reallocating. Not a correctness
// thing — a soak-test concern: a buffer that reallocates on every
// Enter would generate GC pressure on a session with many turns.
func TestLineBuffer_ResetKeepsCapacity(t *testing.T) {
	b := NewLineBuffer()
	for i := 0; i < 100; i++ {
		b.Append('x')
	}
	capBefore := cap(b.runes)
	b.Reset()
	assert.Equal(t, "", b.String())
	assert.Equal(t, capBefore, cap(b.runes),
		"Reset must not shrink the underlying slice; reused for the next input cycle")
}

// TestLineBuffer_TrimRemovesPadding makes the submission contract
// explicit: the agent loop sees "fix bar.go", not "  fix bar.go\t".
// Without Trim, history rows would carry a user's accidental padding
// straight into the LLM prompt.
func TestLineBuffer_TrimRemovesPadding(t *testing.T) {
	b := NewLineBuffer()
	for _, r := range "   fix bar.go\t " {
		b.Append(r)
	}
	assert.Equal(t, "fix bar.go", b.Trim())
}

// TestLineBuffer_CursorMovement walks the cursor through a typed
// line and asserts every position. Each MoveLeft / MoveRight call
// returns true only when the cursor actually moved; the no-op
// returns false so the caller can skip a useless repaint.
func TestLineBuffer_CursorMovement(t *testing.T) {
	b := NewLineBuffer()
	for _, r := range "abc" {
		b.Append(r)
	}
	assert.Equal(t, 3, b.Cursor(), "Append parks the cursor at end")

	assert.True(t, b.MoveLeft())
	assert.Equal(t, 2, b.Cursor())

	assert.True(t, b.MoveLeft())
	assert.True(t, b.MoveLeft())
	assert.Equal(t, 0, b.Cursor())

	assert.False(t, b.MoveLeft(), "no movement at column 0")
	assert.Equal(t, 0, b.Cursor())

	assert.True(t, b.MoveRight())
	assert.Equal(t, 1, b.Cursor())

	assert.True(t, b.MoveEnd())
	assert.Equal(t, 3, b.Cursor())

	assert.False(t, b.MoveEnd(), "no movement when already at end")

	assert.True(t, b.MoveStart())
	assert.Equal(t, 0, b.Cursor())
}

// TestLineBuffer_InsertAtCursor is the arrow-key-aware editing
// path: cursor moves into the middle of the buffer, Insert places
// the new rune there (not at the end). Without this, ←←← Insert
// would silently append, surprising any user with bash muscle
// memory.
func TestLineBuffer_InsertAtCursor(t *testing.T) {
	b := NewLineBuffer()
	for _, r := range "abc" {
		b.Append(r)
	}

	// Move cursor between 'a' and 'b'.
	b.MoveStart()
	b.MoveRight()
	assert.Equal(t, 1, b.Cursor())

	b.Insert('X')
	assert.Equal(t, "aXbc", b.String())
	assert.Equal(t, 2, b.Cursor(), "cursor advances past the inserted rune")
}

// TestLineBuffer_BackspaceAtCursor ensures Backspace removes the
// rune to the LEFT of the cursor, not the trailing rune of the
// whole buffer. Critical when the user has navigated mid-line:
// Backspace must feel like "erase the character I just passed".
func TestLineBuffer_BackspaceAtCursor(t *testing.T) {
	b := NewLineBuffer()
	for _, r := range "abcd" {
		b.Append(r)
	}
	b.MoveStart()
	b.MoveRight()
	b.MoveRight() // cursor between 'b' and 'c'
	assert.Equal(t, 2, b.Cursor())

	assert.True(t, b.Backspace())
	assert.Equal(t, "acd", b.String(), "deletes 'b' to the left of cursor")
	assert.Equal(t, 1, b.Cursor())
}

// TestLineBuffer_DeleteForward exercises the forward-delete (Delete
// key) which removes the rune AT the cursor (the one that would
// next be Inserted-over). Counterpart to Backspace; both must
// preserve cursor position correctly.
func TestLineBuffer_DeleteForward(t *testing.T) {
	b := NewLineBuffer()
	for _, r := range "abc" {
		b.Append(r)
	}
	b.MoveStart() // cursor at 0

	assert.True(t, b.Delete())
	assert.Equal(t, "bc", b.String(), "deletes 'a' at cursor")
	assert.Equal(t, 0, b.Cursor(), "cursor stays put — next rune slid into place")

	b.MoveEnd()
	assert.False(t, b.Delete(), "no-op at end of line")
}

// TestLineBuffer_KillWordAtCursor checks the bash readline behavior:
// Ctrl+W deletes the word BEFORE the cursor, not unconditionally
// from the end. Critical for mid-line edits.
func TestLineBuffer_KillWordAtCursor(t *testing.T) {
	b := NewLineBuffer()
	for _, r := range "fix bar.go now" {
		b.Append(r)
	}
	// Navigate cursor to right after "bar.go ": between space
	// and 'n' of "now".
	b.MoveStart()
	for i := 0; i < 11; i++ {
		b.MoveRight()
	}
	assert.Equal(t, 11, b.Cursor())

	assert.True(t, b.KillWord())
	assert.Equal(t, "fix now", b.String(), "deletes 'bar.go ' before cursor, leaves 'now' intact")
}

// TestLineBuffer_SetContentsParksCursorAtEnd is the history-recall
// path: SetContents loads a recalled entry, and the cursor lands
// after the last rune so the user can keep typing without first
// pressing End.
func TestLineBuffer_SetContentsParksCursorAtEnd(t *testing.T) {
	b := NewLineBuffer()
	b.Append('x') // dirty: prove SetContents replaces, not appends
	b.SetContents("recalled history line")
	assert.Equal(t, "recalled history line", b.String())
	assert.Equal(t, len([]rune("recalled history line")), b.Cursor())
}
