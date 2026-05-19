/*
 * ChatCLI - Coder turn-UI resize / suspend / resume tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResize_RecomputesLayoutAndRepaintsStatus walks the SIGWINCH
// flow: the user resizes from 24x80 to 30x100, the new status row
// (29) gets the cached spinner text. Without the cached repaint the
// status row would be blank until the next timer tick — visible
// glitch on every resize.
func TestResize_RecomputesLayoutAndRepaintsStatus(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	require.NoError(t, u.UpdateStatus("spinning"))
	buf.Reset()

	require.NoError(t, u.Resize(30, 100))
	got := buf.String()

	assert.Contains(t, got, "\x1b[1;28r", "new DECSTBM uses the new rows-2 bound")
	assert.Contains(t, got, "\x1b[29;1H", "status repaints on the new status row (rows-1)")
	assert.Contains(t, got, "spinning", "cached status survives the resize")

	layout := u.Layout()
	assert.Equal(t, 30, layout.Rows)
	assert.Equal(t, 28, layout.ContentBottom)
	assert.Equal(t, 29, layout.StatusRow)
	assert.Equal(t, 30, layout.InputRow)
}

// TestResize_RejectsTooSmallNewSize ensures a resize TO a degenerate
// size leaves the existing layout intact rather than half-applying a
// bad DECSTBM. The user might drag the terminal down to 2 rows and
// back up — we should never lose the spinner because of a transient
// invalid state.
func TestResize_RejectsTooSmallNewSize(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	buf.Reset()

	err := u.Resize(2, 80)
	require.Error(t, err)
	assert.Empty(t, buf.String(), "no terminal traffic on a rejected resize")

	layout := u.Layout()
	assert.Equal(t, 24, layout.Rows, "old layout preserved on rejection")
}

// TestResize_NoOpWhenInactive proves the SIGWINCH handler is safe
// even when the agent has not started a turn yet. Calling Resize
// while inactive silently does nothing — the signal handler doesn't
// need a guard.
func TestResize_NoOpWhenInactive(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Resize(40, 120))
	assert.Empty(t, buf.String())
}

// TestSuspend_TearsDownButKeepsCachedStatus is the security-prompt
// preparation path. After Suspend the UI is not active (so the
// cooked-mode prompt can run untouched), but the cached status is
// still there so Resume can repaint it without the caller juggling
// the message string.
func TestSuspend_TearsDownButKeepsCachedStatus(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	require.NoError(t, u.UpdateStatus("pre-suspend"))
	buf.Reset()

	require.NoError(t, u.Suspend())
	assert.False(t, u.IsActive(), "Suspend deactivates the UI")
	assert.Contains(t, buf.String(), "\x1b[r", "scroll region reset emitted on suspend")
}

// TestSuspend_IsIdempotent matches End's contract for safety in a
// defer chain. A double-Suspend should not double-restore the TTY
// or write the reset twice.
func TestSuspend_IsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Suspend(), "Suspend on inactive UI is a no-op")
	require.NoError(t, u.Begin(24, 80))
	require.NoError(t, u.Suspend())
	require.NoError(t, u.Suspend(), "second Suspend is a no-op")
}

// TestResume_RejectsWhileActive guards the suspend/resume protocol.
// Calling Resume on an already-active UI would re-enter raw mode
// over an existing raw mode and double-enter the region — both
// cause subtle bugs the test catches at the API boundary instead.
func TestResume_RejectsWhileActive(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))

	err := u.Resume(24, 80, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still active")
}

// TestSuspend_PreservesCachedStatusForResume locks the invariant that
// makes the security-prompt flow work: Suspend leaves the cached
// status untouched so Resume can repaint it without the caller
// re-sending the message. End, by contrast, clears it because the
// turn is over.
func TestSuspend_PreservesCachedStatusForResume(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	require.NoError(t, u.UpdateStatus("paused-message"))
	require.NoError(t, u.Suspend())

	// Internal-state assertion via the same-package test: the
	// status survives. End would have wiped it (TestEnd_Idempotent
	// covers that branch separately).
	u.stateMu.Lock()
	cached := u.lastStatus
	u.stateMu.Unlock()
	assert.Equal(t, "paused-message", cached,
		"Suspend must keep lastStatus for Resume; End is the path that clears it")

	// Sanity: End after Suspend should still work (defer-safe).
	require.NoError(t, u.End())
}

// TestResize_PreservesActiveFlag is a small invariant test: a
// successful resize leaves the UI active. A regression here would
// hand the user a "muted" UI that ignores subsequent UpdateStatus.
func TestResize_PreservesActiveFlag(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	require.NoError(t, u.Resize(40, 120))
	assert.True(t, u.IsActive())
}

// TestResize_SuspendBeforeReturnsCleanly confirms that calling
// Resize on a UI that has been Suspended is a no-op (since active
// is false). A SIGWINCH that fires during a security prompt should
// not partially restore the UI.
func TestResize_SuspendBeforeReturnsCleanly(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	require.NoError(t, u.Suspend())
	buf.Reset()

	require.NoError(t, u.Resize(40, 120))
	assert.Empty(t, strings.TrimSpace(buf.String()),
		"no terminal traffic when resizing a suspended UI")
}
