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
