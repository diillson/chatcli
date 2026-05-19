/*
 * ChatCLI - Coder turn-UI orchestrator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
)

// TurnUI is the lifecycle handle for one /coder turn. It owns the
// terminal's scroll region, status row, and the raw stdin input
// loop. The split-pane UX comes from three coordinated layers:
// DECSTBM scroll region confines agent output to the upper band,
// raw-mode stdin lets the mini-readline own the input row without
// kernel echo interference, and a save/restore cursor dance keeps
// the spinner updates from disturbing the input cursor.
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

	// raw holds the cooked-mode termios snapshot captured when
	// BeginInteractive enters raw mode. Restored on End so the
	// terminal returns to its pre-/coder state — leaving raw mode
	// set would break every subsequent command in the same
	// terminal session.
	raw   *rawState
	rawFd int
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

	// Alt-screen FIRST. We want the user's scrollback (everything
	// they had on screen before /coder started — the chat banner,
	// the welcome tip, previous turns) preserved untouched. Alt-
	// screen swaps to a fresh blank canvas; on End we swap back
	// and the user sees their pre-/coder state restored as if
	// nothing happened. Without this, EnterRegion's cursor-home
	// would have to compete with whatever was already on screen
	// and the agent's banner would visually overlap or create
	// gaps — both bugs reported in the field.
	if err := EnterAltScreen(t.out); err != nil {
		return fmt.Errorf("turnui: EnterAltScreen: %w", err)
	}

	if err := EnterRegion(t.out, layout); err != nil {
		// Unwind alt-screen so we don't leave the terminal in a
		// half-state. The defer at the call site can't help here
		// because Begin returned an error and the caller assumes
		// nothing was applied.
		_ = ExitAltScreen(t.out)
		return fmt.Errorf("turnui: EnterRegion: %w", err)
	}

	t.stateMu.Lock()
	t.active = true
	t.layout = layout
	t.lastStatus = ""
	t.stateMu.Unlock()

	return nil
}

// BeginInteractive is the full-featured entry point: it does what
// Begin does AND puts the given file descriptor into raw mode so the
// mini-readline can own keystrokes. Use this when the agent loop
// actually wants the split UI; use Begin alone in tests that only
// exercise the region/status machinery.
//
// On any failure (invalid layout, MakeRaw error) the TurnUI is left
// inert with no terminal traffic emitted, mirroring Begin's contract.
// The caller can fall back to the legacy renderer without a cleanup
// step. A nil return from BeginInteractive establishes the invariant
// that End MUST be called to restore both the region and raw mode.
func (t *TurnUI) BeginInteractive(rows, cols, fd int) error {
	if err := t.Begin(rows, cols); err != nil {
		return err
	}
	raw, err := enterRawMode(fd)
	if err != nil {
		// Unwind the region so the terminal is whole again.
		_ = t.End()
		return fmt.Errorf("turnui: enter raw mode: %w", err)
	}
	t.stateMu.Lock()
	t.raw = raw
	t.rawFd = fd
	t.stateMu.Unlock()
	return nil
}

// End restores the default scroll region, parks the cursor on a
// fresh line below the input row, and restores the cooked-mode TTY
// if BeginInteractive set raw mode. Safe to call when the TurnUI is
// not active (returns nil immediately) — this lets callers defer
// End right after construction without checking whether Begin ever
// succeeded. End never returns an error from "already ended"; only
// terminal write failures and TTY restore failures are surfaced.
//
// The TWO cleanups run unconditionally in sequence: even if the
// region restore fails, raw mode is still restored. Leaking raw mode
// is the worse failure mode by far — the user's next bash command
// would have no echo and no line editing.
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
	raw := t.raw
	rawFd := t.rawFd
	t.active = false
	t.lastStatus = ""
	t.raw = nil
	t.rawFd = 0
	t.stateMu.Unlock()

	t.writeMu.Lock()
	regionErr := ExitRegion(t.out, layout)
	// Always exit alt-screen, even on a region restore failure.
	// Leaving the alt buffer set would hide the user's scrollback
	// forever; that is the highest-severity miss and must run
	// unconditionally. The order is: region first (lifts DECSTBM
	// inside the alt buffer, so the swap-back doesn't carry a
	// clamped region back to the main screen), then alt-screen
	// swap-back.
	altErr := ExitAltScreen(t.out)
	t.writeMu.Unlock()

	rawErr := raw.restore(rawFd)

	// Raw mode failure is the higher-severity miss — surface it
	// first so the caller sees the worst thing that happened.
	if rawErr != nil {
		return fmt.Errorf("turnui: restore raw mode: %w", rawErr)
	}
	if altErr != nil {
		return fmt.Errorf("turnui: exit alt-screen: %w", altErr)
	}
	return regionErr
}

// Run is the convenience entry point that wires BeginInteractive +
// RunReadLine + End together for the typical agent-loop usage:
//
//	ui := turnui.New(os.Stdout)
//	err := ui.Run(ctx, turnui.RunConfig{
//	    Rows: h, Cols: w,
//	    OnSubmit: func(line string) { … push to agent queue … },
//	})
//
// The function blocks until the input loop ends (Ctrl+D, ctx cancel,
// or read error) and guarantees End fires exactly once via defer.
// Errors from any phase are wrapped so the caller can distinguish
// activation failures (fall back) from runtime failures (already in
// the split UI, must restore before reporting).
func (t *TurnUI) Run(ctx context.Context, cfg RunConfig) error {
	if err := t.BeginInteractive(cfg.Rows, cfg.Cols, cfg.StdinFD); err != nil {
		return err
	}
	defer func() { _ = t.End() }()

	reader := cfg.Reader
	if reader == nil {
		reader = os.Stdin
	}

	return RunReadLine(ctx, ReadLineConfig{
		Reader:   reader,
		Painter:  t,
		OnSubmit: cfg.OnSubmit,
		OnCancel: cfg.OnCancel,
	})
}

// RunConfig bundles the inputs Run needs. The Reader field is
// optional and defaults to os.Stdin — tests inject a scripted reader
// to drive the loop without a TTY.
type RunConfig struct {
	Rows     int
	Cols     int
	StdinFD  int
	Reader   io.Reader
	OnSubmit func(line string)
	OnCancel func()
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

// Painter exposes the TurnUI as an InputPainter for callers that
// spawn their own readline goroutine after BeginInteractive (the
// agent loop does this so it can wire the OnSubmit callback into
// its own queue plumbing without going through TurnUI.Run). The
// returned value is the TurnUI itself; nothing is allocated.
func (t *TurnUI) Painter() InputPainter { return t }

// Resize recomputes the layout for new terminal dimensions and re-
// enters the scroll region so subsequent agent output respects the
// new bounds. Called by the SIGWINCH handler when the user drags the
// terminal window. The cached status is repainted on the new status
// row; the input row is NOT repainted here (the readline loop owns
// the input buffer and will repaint on the next keystroke or via an
// explicit Refresh from the caller).
//
// On invalid new dimensions (too small after the resize) Resize
// returns an error WITHOUT touching the terminal — the agent loop is
// expected to either keep the previous layout or fall back to legacy
// rendering. Half-applying a too-small layout would leave the user
// staring at overlapping bands.
func (t *TurnUI) Resize(rows, cols int) error {
	newLayout := NewLayout(rows, cols)
	if !newLayout.Valid() {
		return fmt.Errorf("turnui: post-resize layout too small (%dx%d)", rows, cols)
	}

	t.stateMu.Lock()
	if !t.active {
		t.stateMu.Unlock()
		return nil
	}
	cachedStatus := t.lastStatus
	t.layout = newLayout
	t.stateMu.Unlock()

	t.writeMu.Lock()
	if err := EnterRegion(t.out, newLayout); err != nil {
		t.writeMu.Unlock()
		return fmt.Errorf("turnui: re-enter region after resize: %w", err)
	}
	t.writeMu.Unlock()

	if cachedStatus != "" {
		if err := t.UpdateStatus(cachedStatus); err != nil {
			return err
		}
	}
	return nil
}

// Suspend tears down the split-UI state (raw mode + scroll region)
// WITHOUT clearing the cached status. Used by the security-prompt
// path: the user is about to see a "allow / deny / always" dialog
// that wants cooked-mode line editing — running it inside the split
// UI would force the dialog to fight turnui for keystrokes and
// duplicate the bottom row.
//
// After Suspend the TurnUI is no longer active, but the cached
// status survives so Resume can repaint without the caller having
// to remember it. Suspend is idempotent: a second call is a no-op.
//
// The caller is responsible for stopping any readline goroutine it
// spawned before calling Suspend — TurnUI itself does not own the
// goroutine; that lives in the caller's setup code.
func (t *TurnUI) Suspend() error {
	t.stateMu.Lock()
	if !t.active {
		t.stateMu.Unlock()
		return nil
	}
	layout := t.layout
	raw := t.raw
	rawFd := t.rawFd
	t.active = false
	t.raw = nil
	t.rawFd = 0
	// Keep lastStatus intact for Resume to repaint.
	t.stateMu.Unlock()

	t.writeMu.Lock()
	regionErr := ExitRegion(t.out, layout)
	// Exit alt-screen so the security prompt renders on the
	// original screen the user is used to (with their cooked-mode
	// echo and visible scrollback). Resume re-enters alt-screen,
	// re-applies the region, and repaints the cached status.
	altErr := ExitAltScreen(t.out)
	t.writeMu.Unlock()

	rawErr := raw.restore(rawFd)

	if rawErr != nil {
		return fmt.Errorf("turnui: suspend raw mode restore: %w", rawErr)
	}
	if altErr != nil {
		return fmt.Errorf("turnui: suspend alt-screen exit: %w", altErr)
	}
	return regionErr
}

// Resume re-enters the split UI after a Suspend / cooked-mode
// excursion. Re-runs BeginInteractive with the original dimensions
// and repaints the cached status. The input row is NOT repainted
// here — the caller is expected to re-spawn the readline goroutine,
// whose initial PaintInput call will draw the empty input row.
//
// On a failed Resume the TurnUI stays inactive and returns the
// error; the caller can fall back to legacy mode for the rest of
// the session. Half-resumed state is not a thing — either Resume
// succeeds and the UI is whole, or it fails and the caller knows.
func (t *TurnUI) Resume(rows, cols, fd int) error {
	t.stateMu.Lock()
	if t.active {
		t.stateMu.Unlock()
		return fmt.Errorf("turnui: Resume called while still active")
	}
	cachedStatus := t.lastStatus
	t.stateMu.Unlock()

	if err := t.BeginInteractive(rows, cols, fd); err != nil {
		return err
	}
	if cachedStatus != "" {
		if err := t.UpdateStatus(cachedStatus); err != nil {
			return err
		}
	}
	return nil
}

// InputPrompt is the prefix that appears on the input row before the
// user's typed text. Hard-coded for Phase B; future phases may make
// it user-configurable. Chosen to match the visual idiom used by chat
// mode's go-prompt prefix so users do not have to context-switch.
const InputPrompt = "❯ "

// PaintInput repaints the input row: cursor to (InputRow, 1), clear,
// write prompt + buffer contents, then position the cursor at the
// column matching the buffer's logical cursor (which can be anywhere
// in the buffer after Phase G's arrow-key navigation, not just at
// the end). Required by the InputPainter interface that RunReadLine
// uses.
//
// Unlike paintStatus, PaintInput does NOT save/restore the cursor —
// the input row IS the cursor's home in the split UI. After this
// returns the cursor sits at the column where the next keystroke
// will insert / overwrite.
//
// Cursor column math: InputPrompt is two visible columns ("❯ ") plus
// the buffer cursor offset. Both are 1-based on the wire (CUP uses
// 1 for the leftmost column), so the final column is
// promptWidth + buf.Cursor() + 1.
func (t *TurnUI) PaintInput(buf *LineBuffer) error {
	t.stateMu.Lock()
	if !t.active {
		t.stateMu.Unlock()
		return nil
	}
	layout := t.layout
	t.stateMu.Unlock()

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	if err := MoveCursor(t.out, layout.InputRow, 1); err != nil {
		return err
	}
	if err := ClearLine(t.out); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(t.out, "%s%s", InputPrompt, buf.String()); err != nil {
		return err
	}
	// Position the cursor at the logical insertion point. The
	// prompt "❯ " is two visible columns; the buffer cursor adds
	// its rune offset. CUP is 1-based, so the final col is
	// promptWidth (2) + cursorOffset + 1. The MoveCursor here is
	// what makes arrow-key navigation visible — without it the
	// terminal cursor would sit after the last rune painted even
	// when the user navigated mid-line, and Insert would appear
	// to add characters at the end instead of where the cursor
	// logically is.
	return MoveCursor(t.out, layout.InputRow, 2+buf.Cursor()+1)
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
