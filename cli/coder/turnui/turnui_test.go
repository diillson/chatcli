/*
 * ChatCLI - Coder turn-UI orchestrator tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBegin_EntersAltScreenThenRegionAndMarksActive is the happy
// path: a Begin against a typical terminal must succeed, leave the
// TurnUI in the active state, and have already written the alt-
// screen entry + DECSTBM + CUP sequence to the byte stream in that
// order. End must produce the matching teardown in the reverse
// order: region reset before alt-screen exit, so the swap-back
// does not carry a clamped region into the main screen.
func TestBegin_EntersAltScreenThenRegionAndMarksActive(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)

	require.NoError(t, u.Begin(24, 80))
	assert.True(t, u.IsActive())

	got := buf.String()
	altIdx := strings.Index(got, "\x1b[?1049h")
	regionIdx := strings.Index(got, "\x1b[1;22r")
	require.NotEqual(t, -1, altIdx, "alt-screen entry missing on Begin")
	require.NotEqual(t, -1, regionIdx, "DECSTBM missing on Begin")
	assert.Less(t, altIdx, regionIdx,
		"alt-screen MUST be entered before the region is defined — defining the region first would clamp the user's main screen")

	buf.Reset()
	require.NoError(t, u.End())
	assert.False(t, u.IsActive())

	got = buf.String()
	regionResetIdx := strings.Index(got, "\x1b[r")
	altExitIdx := strings.Index(got, "\x1b[?1049l")
	require.NotEqual(t, -1, regionResetIdx, "region reset missing on End")
	require.NotEqual(t, -1, altExitIdx, "alt-screen exit missing on End")
	assert.Less(t, regionResetIdx, altExitIdx,
		"region reset MUST come before alt-screen exit — otherwise DECSTBM persists into the user's main screen after swap-back")
}

// TestBegin_RejectsTooSmallTerminal pins the contract: undersized
// terminals must not emit any bytes. The caller relies on this to
// know that "Begin failed" means "your terminal is untouched, fall
// back cleanly" — not "best guess, might be half-applied."
func TestBegin_RejectsTooSmallTerminal(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)

	err := u.Begin(2, 80)
	require.Error(t, err)
	assert.False(t, u.IsActive())
	assert.Empty(t, buf.String(), "no terminal traffic for an invalid layout")
}

// TestBegin_FailsOnDoubleActivation catches the bug where a caller
// forgets to End between turns. Without the guard, the second Begin
// would issue another DECSTBM (harmless on its own) but would also
// reset lastStatus and the active flag's "second activation" path —
// downstream code that assumes "active means we ended the previous
// turn cleanly" would be wrong. Failing loudly is the safer default.
func TestBegin_FailsOnDoubleActivation(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)

	require.NoError(t, u.Begin(24, 80))
	err := u.Begin(24, 80)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already active")
}

// TestEnd_IsIdempotent lets callers defer End without checking
// whether Begin succeeded. A defer-everywhere pattern is the only
// realistic way to keep the terminal sane across panics and early
// returns; End must therefore be safe to call any number of times.
func TestEnd_IsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)

	require.NoError(t, u.End(), "End on a fresh TurnUI is a no-op")
	require.NoError(t, u.Begin(24, 80))
	require.NoError(t, u.End())
	require.NoError(t, u.End(), "second End after Begin/End is a no-op")
}

// TestUpdateStatus_DrawsAtStatusRowThenSnapsToInputRow walks the
// new redraw sequence: move to status row, clear, write message,
// then move to the input row at the column PaintInput last
// recorded. Earlier versions used ESC 7 / ESC 8 save/restore but
// the xterm.js-based Hyper terminal honors those inconsistently;
// the explicit MoveCursor approach works everywhere CUP works
// (which is every VT100-derived emulator).
func TestUpdateStatus_DrawsAtStatusRowThenSnapsToInputRow(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))

	// Simulate a PaintInput having recorded a cursor column.
	// Without this, UpdateStatus falls back to "prompt + 1" which
	// is the right default but not what we want to assert here.
	u.lastInputCol.Store(7)

	buf.Reset() // discard the Begin bytes; we only care about the status redraw

	require.NoError(t, u.UpdateStatus("⠹ working"))

	got := buf.String()
	moveStatus := strings.Index(got, "\x1b[23;1H")  // status row = rows-1
	clearIdx := strings.Index(got, "\x1b[2K")
	msgIdx := strings.Index(got, "⠹ working")
	moveBack := strings.Index(got, "\x1b[24;7H") // input row = rows, col from lastInputCol

	require.NotEqual(t, -1, moveStatus, "missing MoveCursor to status row")
	require.NotEqual(t, -1, clearIdx, "missing ClearLine")
	require.NotEqual(t, -1, msgIdx, "missing status message body")
	require.NotEqual(t, -1, moveBack, "missing MoveCursor back to input row")

	// Order: move to status → clear → write → move back to input.
	assert.Less(t, moveStatus, clearIdx)
	assert.Less(t, clearIdx, msgIdx)
	assert.Less(t, msgIdx, moveBack)

	// Crucially, NO save/restore sequences in the wire output.
	// If ESC 7 / 8 leak back in, terminals that ignore them would
	// regress to the cascading-spinner behavior we just fixed.
	assert.NotContains(t, got, "\x1b7", "must not emit ESC 7 (DECSC) — unreliable on xterm.js")
	assert.NotContains(t, got, "\x1b8", "must not emit ESC 8 (DECRC) — unreliable on xterm.js")
}

// TestUpdateStatus_NoOpWhenInactive matches the documented "drop
// silently when not active" contract. The timer callback in the
// agent loop calls UpdateStatus from a goroutine that has no easy
// way to know whether Begin succeeded; UpdateStatus must therefore
// not require a check on its caller's part.
func TestUpdateStatus_NoOpWhenInactive(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.UpdateStatus("ignored"))
	assert.Empty(t, buf.String())
}

// TestRefresh_RepaintsLastStatus simulates the SIGWINCH path: after
// a resize the layout changes and the spinner needs to land on the
// new status row. Refresh must use the cached message so the caller
// (the SIGWINCH handler in Phase D) does not have to remember it.
func TestRefresh_RepaintsLastStatus(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	require.NoError(t, u.UpdateStatus("cached"))

	buf.Reset()
	require.NoError(t, u.Refresh())
	assert.Contains(t, buf.String(), "cached", "Refresh repaints the cached status")
}

// TestRefresh_NoOpWithNoCachedStatus is the boundary case: a
// SIGWINCH that fires before the first status update should not
// paint a stale empty line. Empty cached status means "nothing to
// repaint, leave the row alone."
func TestRefresh_NoOpWithNoCachedStatus(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	buf.Reset()

	require.NoError(t, u.Refresh())
	assert.Empty(t, buf.String(), "no cached status, no redraw")
}

// TestUpdateStatus_ConcurrentCallsAreSerialized fires N goroutines
// that each call UpdateStatus and asserts the final buffer contains
// only well-formed redraw sequences — no interleaved bytes from two
// concurrent paints. The data race detector (go test -race) is the
// real watchdog; this test ensures it has something to watch.
func TestUpdateStatus_ConcurrentCallsAreSerialized(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	buf.Reset()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_ = u.UpdateStatus("tick")
		}(i)
	}
	wg.Wait()

	// Every paint emits exactly one move-to-status (CUP to row
	// 23 col 1) and one move-back-to-input (CUP to row 24). If
	// serialization is broken the counts diverge — a missing
	// move-back would mean a paint stomped on another mid-
	// sequence, leaving subsequent agent output to land on the
	// status row.
	got := buf.String()
	statusMoves := strings.Count(got, "\x1b[23;1H")
	inputMoves := strings.Count(got, "\x1b[24;")
	assert.Equal(t, statusMoves, inputMoves,
		"every move-to-status must be matched by a move-back-to-input — broken serialization corrupts that 1:1")
}
