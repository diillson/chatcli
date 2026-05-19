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

// TestBegin_EntersRegionAndMarksActive is the happy path: a Begin
// against a typical terminal must succeed, leave the TurnUI in the
// active state, and have already written the DECSTBM + CUP sequence
// to the byte stream. End must produce the matching teardown.
func TestBegin_EntersRegionAndMarksActive(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)

	require.NoError(t, u.Begin(24, 80))
	assert.True(t, u.IsActive())
	assert.Contains(t, buf.String(), "\x1b[1;22r", "DECSTBM written on Begin")

	buf.Reset()
	require.NoError(t, u.End())
	assert.False(t, u.IsActive())
	assert.Contains(t, buf.String(), "\x1b[r", "region reset written on End")
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

// TestUpdateStatus_DrawsAtStatusRow walks the redraw sequence on a
// captured buffer. The status row for a 24-row terminal is 23, and
// the sequence must be: save cursor, move to (23,1), clear line,
// write the message, restore cursor — in that order. Order matters
// because a misplaced save would capture the post-redraw cursor
// position and the restore would land in the wrong band.
func TestUpdateStatus_DrawsAtStatusRow(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	buf.Reset() // discard the Begin bytes; we only care about the status redraw

	require.NoError(t, u.UpdateStatus("⠹ working"))

	got := buf.String()
	// Save → Move → Clear → Message → Restore. Asserting indexes
	// instead of an exact-string match keeps the test robust to
	// future additions to the status sequence (e.g. an attribute
	// reset prefix) while still locking the protocol order.
	saveIdx := strings.Index(got, "\x1b7")
	moveIdx := strings.Index(got, "\x1b[23;1H")
	clearIdx := strings.Index(got, "\x1b[2K")
	msgIdx := strings.Index(got, "⠹ working")
	restoreIdx := strings.Index(got, "\x1b8")

	require.NotEqual(t, -1, saveIdx, "missing SaveCursor")
	require.NotEqual(t, -1, moveIdx, "missing MoveCursor to status row")
	require.NotEqual(t, -1, clearIdx, "missing ClearLine")
	require.NotEqual(t, -1, msgIdx, "missing status message body")
	require.NotEqual(t, -1, restoreIdx, "missing RestoreCursor")

	assert.Less(t, saveIdx, moveIdx)
	assert.Less(t, moveIdx, clearIdx)
	assert.Less(t, clearIdx, msgIdx)
	assert.Less(t, msgIdx, restoreIdx)
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

	// Every paint emits exactly one SaveCursor (\x1b7) followed by
	// one RestoreCursor (\x1b8). If serialization is broken the
	// counts diverge — a missing pair would mean a paint stomped
	// on another mid-sequence.
	got := buf.String()
	assert.Equal(t, strings.Count(got, "\x1b7"), strings.Count(got, "\x1b8"),
		"every save must be matched by a restore — broken serialization corrupts that 1:1")
}
