/*
 * ChatCLI - Coder turn-UI region tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewLayout_BandsAddUp documents the contract every consumer
// relies on: the three bands cover the whole screen with no overlap
// and no gap. Off-by-one here would either leak agent output into the
// status row or hide the bottom row of content under the input.
func TestNewLayout_BandsAddUp(t *testing.T) {
	l := NewLayout(24, 80)
	assert.Equal(t, 1, l.ContentTop, "content always starts at row 1")
	assert.Equal(t, 22, l.ContentBottom, "rows-2 leaves the status and input rows free")
	assert.Equal(t, 23, l.StatusRow, "status sits one above the input")
	assert.Equal(t, 24, l.InputRow, "input is always the bottom-most row")
	assert.True(t, l.Valid())
}

// TestLayout_ValidRejectsDegenerate makes sure layouts that would
// produce malformed DECSTBM sequences are caught at the boundary,
// not by the terminal. A negative ContentBottom passed into
// EnterRegion would emit "\x1b[1;-1r" which on most terminals
// silently does nothing — leaving the spinner to draw at fake row
// positions and the user staring at a mangled screen.
func TestLayout_ValidRejectsDegenerate(t *testing.T) {
	cases := []struct {
		name string
		l    Layout
		want bool
	}{
		{"too few rows", NewLayout(2, 80), false},
		{"zero rows", NewLayout(0, 80), false},
		{"zero cols", NewLayout(24, 0), false},
		{"exactly min", NewLayout(3, 1), true},
		{"typical", NewLayout(24, 80), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.l.Valid())
		})
	}
}

// TestEnterRegion_EmitsDECSTBMAndCursorHome is the regression test for
// the exact byte sequence that confines scrolling. The terminal sees
// this as: "set margins to 1..22, jump to top-left." A typo in either
// number breaks the band assignment silently — there is no error
// returned by the terminal, just a wrecked UI — so we assert the
// bytes directly. The escape character is rendered as \x1b in source;
// the wire format is a single 0x1b byte.
func TestEnterRegion_EmitsDECSTBMAndCursorHome(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, EnterRegion(&buf, NewLayout(24, 80)))

	got := buf.String()
	assert.Contains(t, got, "\x1b[1;22r", "DECSTBM with top=1 bot=22")
	assert.Contains(t, got, "\x1b[H", "cursor home after the margin set")
	// Order matters: DECSTBM first, then cursor-home. Reversing
	// them can cause the cursor to jump back into the just-clamped
	// region from an out-of-band starting position on some emulators.
	assert.Less(t, strings.Index(got, "\x1b[1;22r"), strings.Index(got, "\x1b[H"),
		"DECSTBM must precede cursor-home")
}

// TestEnterRegion_RejectsInvalidLayout confirms the gate at the top
// of EnterRegion fires before any bytes leak to the terminal. A test
// without this guard would let an invalid-layout caller corrupt the
// real terminal during dev iteration.
func TestEnterRegion_RejectsInvalidLayout(t *testing.T) {
	var buf bytes.Buffer
	err := EnterRegion(&buf, NewLayout(2, 80)) // too few rows
	require.Error(t, err)
	assert.Empty(t, buf.String(), "no bytes should escape when the layout is invalid")
}

// TestExitRegion_RestoresDefaultsAndParksCursor verifies the teardown
// path leaves the terminal in a state the legacy renderer can take
// over. The default region reset (\x1b[r) must come first; cursor
// parking second; trailing newline third — that order is what every
// terminal we have tested agrees on for "back to normal scrolling
// with a fresh empty line ready".
func TestExitRegion_RestoresDefaultsAndParksCursor(t *testing.T) {
	var buf bytes.Buffer
	layout := NewLayout(24, 80)
	require.NoError(t, ExitRegion(&buf, layout))

	got := buf.String()
	assert.True(t, strings.HasPrefix(got, "\x1b[r"), "region reset must come first")
	assert.Contains(t, got, "\x1b[24;1H", "cursor parked at the input row")
	assert.True(t, strings.HasSuffix(got, "\n"), "trailing newline so the next prompt is on a clean line")
}

// TestMoveCursor_FormatsRowColumn pins the wire format for CUP. A
// stray comma or a 0-indexed off-by-one here puts the spinner one
// row away from where the layout expects it and the status overwrites
// content.
func TestMoveCursor_FormatsRowColumn(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, MoveCursor(&buf, 12, 5))
	assert.Equal(t, "\x1b[12;5H", buf.String())
}

// TestSaveRestoreCursor_UsesDECVariants pins the choice of ESC 7 / 8
// over CSI s / u. This is intentional and load-bearing: the GoLand
// integrated terminal — which is what triggered the spinner cascade
// bug report — ignores CSI s but honors ESC 7. Any future "cleanup"
// PR that swaps these for the CSI variants would re-introduce the
// regression silently; this test makes that visible.
func TestSaveRestoreCursor_UsesDECVariants(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, SaveCursor(&buf))
	require.NoError(t, RestoreCursor(&buf))
	assert.Equal(t, "\x1b7\x1b8", buf.String())
}

// TestClearLine_EmitsCSI2K nails the erase-line sequence. CSI 2K
// (erase whole line, cursor stays) differs from CSI 0K (erase to end
// of line) and CSI 1K (erase to beginning); picking the wrong K
// leaves the spinner's previous frame partly visible behind the new
// one as a tail of stale glyphs.
func TestClearLine_EmitsCSI2K(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, ClearLine(&buf))
	assert.Equal(t, "\x1b[2K", buf.String())
}

// TestEnterRegion_PropagatesWriteError ensures a half-written DECSTBM
// does not silently succeed. If the writer fails after the margin set
// but before the cursor home, the terminal would be left clamped with
// no cursor placement — the caller MUST see the error so cleanup can
// run.
func TestEnterRegion_PropagatesWriteError(t *testing.T) {
	w := &failingWriter{failAfter: 1} // succeed on the first write, fail on the second
	err := EnterRegion(w, NewLayout(24, 80))
	require.Error(t, err)
}

// failingWriter is a tiny io.Writer that fails the Nth call so the
// region primitives can prove they bail on the first error instead
// of plowing through and leaving a half-applied state.
type failingWriter struct {
	calls     int
	failAfter int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.calls > f.failAfter {
		return 0, errSimulated
	}
	return len(p), nil
}

var errSimulated = errors.New("simulated write failure")
