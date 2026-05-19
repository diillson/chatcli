/*
 * ChatCLI - Coder turn-UI history tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHistory_AppendUpDownRoundtrip is the canonical happy path:
// submit three lines, ↑↑↑ to the oldest, ↓↓↓ back to the original
// buffer. Without this contract every history navigation would
// leave the user wondering whether the buffer was their typing or
// a recalled entry.
func TestHistory_AppendUpDownRoundtrip(t *testing.T) {
	h := NewHistory(10)
	h.Append("first")
	h.Append("second")
	h.Append("third")

	// First ↑ surfaces the newest entry.
	got, ok := h.Up("draft")
	assert.True(t, ok)
	assert.Equal(t, "third", got)

	// Second ↑ surfaces the middle.
	got, ok = h.Up("ignored on subsequent Up")
	assert.True(t, ok)
	assert.Equal(t, "second", got)

	// Third ↑ surfaces the oldest.
	got, ok = h.Up("ignored")
	assert.True(t, ok)
	assert.Equal(t, "first", got)

	// Fourth ↑ has nothing to surface — stays at the oldest.
	_, ok = h.Up("ignored")
	assert.False(t, ok)

	// ↓ walks back forward.
	got, ok = h.Down()
	assert.True(t, ok)
	assert.Equal(t, "second", got)

	got, ok = h.Down()
	assert.True(t, ok)
	assert.Equal(t, "third", got)

	// One more ↓ restores the original buffer ("draft" captured
	// when navigation began).
	got, ok = h.Down()
	assert.True(t, ok)
	assert.Equal(t, "draft", got)

	// Further ↓ returns false — not navigating anymore.
	_, ok = h.Down()
	assert.False(t, ok)
}

// TestHistory_SkipsDuplicates matches bash HISTCONTROL=ignoreboth:
// pressing Enter on the same line twice produces only one entry.
// Without this the user's ↑ would have to scroll past N copies of
// the last command they spammed.
func TestHistory_SkipsDuplicates(t *testing.T) {
	h := NewHistory(10)
	h.Append("same")
	h.Append("same")
	h.Append("same")
	assert.Equal(t, 1, h.Len(), "duplicate consecutive lines collapse")

	h.Append("different")
	h.Append("same")
	assert.Equal(t, 3, h.Len(), "duplicate only collapses when it's the immediately preceding entry")
}

// TestHistory_SkipsEmpty rejects empty / whitespace-only submissions.
// The agent loop already filters these before submitting, but the
// history is the second line of defense: if a future caller bypasses
// the filter, we don't want a phantom blank entry in the stack.
func TestHistory_SkipsEmpty(t *testing.T) {
	h := NewHistory(10)
	h.Append("")
	assert.Equal(t, 0, h.Len())
}

// TestHistory_EvictsAtCap is the FIFO contract under cap pressure.
// Submit cap+5 lines, assert the oldest 5 are gone and the newest 10
// survive. Catches an off-by-one in the eviction loop or a missing
// cap check.
func TestHistory_EvictsAtCap(t *testing.T) {
	h := NewHistory(5)
	for _, s := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		h.Append(s)
	}
	assert.Equal(t, 5, h.Len())

	// Walk all the way back: should see g, f, e, d, c (not b or a).
	want := []string{"g", "f", "e", "d", "c"}
	for i, expected := range want {
		got, ok := h.Up("draft")
		assert.True(t, ok, "step %d", i)
		assert.Equal(t, expected, got, "step %d", i)
	}
	_, ok := h.Up("draft")
	assert.False(t, ok, "no entries past the cap survive")
}

// TestHistory_AppendResetsNavigation guards against a subtle bug:
// the user navigates back to "old", then submits a new line. The
// next ↑ should surface the NEW line, not pick up where the old
// navigation left off. Without this reset the user would see ↑
// surface "old" again immediately after submitting.
func TestHistory_AppendResetsNavigation(t *testing.T) {
	h := NewHistory(10)
	h.Append("first")
	h.Append("second")
	_, _ = h.Up("draft")
	_, _ = h.Up("draft") // now at "first"

	h.Append("third")

	got, ok := h.Up("new draft")
	assert.True(t, ok)
	assert.Equal(t, "third", got, "navigation resets to newest after Append")
}

// TestHistory_ResetClearsNavigationOnly checks that Reset wipes the
// cursor but keeps the entries. Used by the readline loop after
// Ctrl+C to clear navigation state without losing the history the
// user has built up.
func TestHistory_ResetClearsNavigationOnly(t *testing.T) {
	h := NewHistory(10)
	h.Append("keep me")
	_, _ = h.Up("draft")
	h.Reset()

	assert.Equal(t, 1, h.Len())
	got, ok := h.Up("fresh draft")
	assert.True(t, ok)
	assert.Equal(t, "keep me", got)
}

// TestHistory_EmptyUpReturnsFalse is the boundary case: no entries,
// ↑ should NOT replace the buffer with an empty string. The bool
// return is what protects the buffer from a spurious wipe.
func TestHistory_EmptyUpReturnsFalse(t *testing.T) {
	h := NewHistory(10)
	got, ok := h.Up("draft")
	assert.False(t, ok)
	assert.Equal(t, "", got)
}

// TestNewHistory_NonPositiveCapUsesDefault catches the configuration
// mistake of NewHistory(0) (or negative). The default cap kicks in
// so the caller never has a "history that throws away every entry"
// foot-gun.
func TestNewHistory_NonPositiveCapUsesDefault(t *testing.T) {
	for _, c := range []int{0, -1, -100} {
		h := NewHistory(c)
		assert.Equal(t, DefaultHistoryCap, h.cap, "non-positive cap %d falls back to default", c)
	}
}
