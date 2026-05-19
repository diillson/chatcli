/*
 * ChatCLI - Coder turn-UI orchestrator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"fmt"
	"io"
	"sync"
)

// TurnUI is the lifecycle handle for one /coder turn. It owns the
// terminal's scroll region, status row, and (in later phases) the raw
// stdin loop. Phase A wires only the scroll-region machinery — the
// content band and the status row are usable, but input still goes
// through the cooked-mode kernel echo on the input row.
//
// Construction is always cheap and side-effect free; Begin is what
// actually touches the terminal. End is idempotent and safe to call
// from a defer — leaving a TurnUI un-ended would persist the DECSTBM
// region across processes and is exactly the failure mode this type
// is designed to prevent.
//
// Goroutine safety: Begin / End are intended to run on the agent's
// main goroutine, while UpdateStatus may be invoked from the timer
// callback goroutine. All writes route through writeMu so a status
// redraw cannot interleave with the begin/end sequence and produce
// half-written escape codes.
type TurnUI struct {
	// out is the byte sink — always os.Stdout in production, an
	// in-memory buffer in tests. The TurnUI never reads from out,
	// so any io.Writer suffices.
	out io.Writer

	// writeMu serializes every byte the TurnUI emits. The status
	// goroutine, the begin / end sequence, and (future) raw input
	// echo all grab this before issuing escapes. Without it a tick
	// that fires mid-Begin would write a status update into a half-
	// initialized region and the layout would be off until the next
	// SIGWINCH resync.
	writeMu sync.Mutex

	// stateMu guards the lifecycle fields (active, layout, lastStatus).
	// Separating it from writeMu lets UpdateStatus check "are we
	// active" without holding the slower write lock; if the answer
	// is "no" the call is a no-op with no terminal traffic at all.
	stateMu sync.Mutex
	active  bool
	layout  Layout

	// lastStatus is the most recent message the spinner painted.
	// Cached so SIGWINCH-triggered redraws can repaint without
	// requiring the caller to re-send the current text. Mutated
	// under stateMu, read under stateMu by Refresh.
	lastStatus string
}

// New constructs a TurnUI bound to the given writer. The writer is
// retained for the life of the TurnUI; callers should pass os.Stdout
// in production. No terminal traffic is emitted until Begin runs.
func New(out io.Writer) *TurnUI {
	return &TurnUI{out: out}
}

// Begin computes the layout for the given terminal dimensions, sets
// the DECSTBM scroll region, and parks the cursor on the first row
// of the content band. It returns an error if the layout is invalid
// (terminal too small) or if any write to the output stream fails;
// in both cases the terminal is guaranteed to be untouched, so the
// caller can fall back to the legacy single-line renderer without a
// cleanup step.
//
// Calling Begin twice without an intervening End is a programming
// error; the second call returns an error rather than silently
// re-entering the region (which would leave the first region's
// state orphaned). Tests rely on this invariant to flag accidental
// double-activation.
func (t *TurnUI) Begin(rows, cols int) error {
	layout := NewLayout(rows, cols)
	if !layout.Valid() {
		return fmt.Errorf("turnui: terminal too small for split UI (%dx%d)", rows, cols)
	}

	t.stateMu.Lock()
	if t.active {
		t.stateMu.Unlock()
		return fmt.Errorf("turnui: Begin called while already active")
	}
	t.stateMu.Unlock()

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if err := EnterRegion(t.out, layout); err != nil {
		return fmt.Errorf("turnui: EnterRegion: %w", err)
	}

	t.stateMu.Lock()
	t.active = true
	t.layout = layout
	t.lastStatus = ""
	t.stateMu.Unlock()

	return nil
}

// End restores the default scroll region and parks the cursor on a
// fresh line below the input row. Safe to call when the TurnUI is
// not active (returns nil immediately) — this lets callers defer
// End right after construction without checking whether Begin ever
// succeeded. End never returns an error from "already ended"; only
// terminal write failures are surfaced.
//
// After End the TurnUI may be re-Begin'd for a subsequent turn. The
// instance is reusable; nothing in TurnUI assumes single-shot.
func (t *TurnUI) End() error {
	t.stateMu.Lock()
	if !t.active {
		t.stateMu.Unlock()
		return nil
	}
	layout := t.layout
	t.active = false
	t.lastStatus = ""
	t.stateMu.Unlock()

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	return ExitRegion(t.out, layout)
}

// UpdateStatus paints msg on the dedicated status row. If the TurnUI
// is not active the call is silently dropped — this matches the
// "graceful degradation" contract callers rely on when the legacy
// fallback is in effect (the timer callback can blindly call
// UpdateStatus without first checking IsActive).
//
// The redraw sequence saves the cursor, jumps to the status row,
// clears the line, writes the message, and restores the cursor.
// SaveCursor/RestoreCursor uses the DEC ESC 7 / ESC 8 pair (see
// region.go) because the CSI s/u variants are unreliable on the
// terminals where this branch was first reported broken.
func (t *TurnUI) UpdateStatus(msg string) error {
	t.stateMu.Lock()
	if !t.active {
		t.stateMu.Unlock()
		return nil
	}
	layout := t.layout
	t.lastStatus = msg
	t.stateMu.Unlock()

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	return paintStatus(t.out, layout, msg)
}

// Refresh repaints the cached status message. Used after a SIGWINCH
// resync to put the spinner back on the status row without making
// the agent loop hand the message in again. A no-op when there is no
// cached status (the first tick after Begin will populate it).
func (t *TurnUI) Refresh() error {
	t.stateMu.Lock()
	if !t.active {
		t.stateMu.Unlock()
		return nil
	}
	layout := t.layout
	msg := t.lastStatus
	t.stateMu.Unlock()

	if msg == "" {
		return nil
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	return paintStatus(t.out, layout, msg)
}

// IsActive reports whether Begin has succeeded and End has not yet
// run. Callers use it to gate optional features (the runaway-guard
// banner, for example, prints differently inside the split UI than
// in the legacy fallback).
func (t *TurnUI) IsActive() bool {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.active
}

// Layout returns the current layout snapshot. The returned value is a
// copy so callers cannot mutate the live instance. Returns the zero
// Layout when not active.
func (t *TurnUI) Layout() Layout {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return t.layout
}

// paintStatus is the bare-bytes status redraw. Pulled out so tests
// can exercise the sequence without juggling locks. The function
// assumes the caller holds writeMu — concurrent callers would
// interleave save/restore and corrupt the cursor state.
func paintStatus(w io.Writer, layout Layout, msg string) error {
	if err := SaveCursor(w); err != nil {
		return err
	}
	if err := MoveCursor(w, layout.StatusRow, 1); err != nil {
		return err
	}
	if err := ClearLine(w); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, msg); err != nil {
		return err
	}
	return RestoreCursor(w)
}
