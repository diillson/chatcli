/*
 * ChatCLI - Coder turn-UI scroll region primitives
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"fmt"
	"io"
)

// The split UI carves the terminal into three horizontal bands:
//
//   ┌─────────────────────────────┐
//   │ rows 1 .. contentBottom     │  ← agent output, scrolls inside
//   │ row  contentBottom+1        │  ← status (spinner, queue depth)
//   │ row  rows                   │  ← input prompt
//   └─────────────────────────────┘
//
// The DECSTBM "Set Top and Bottom Margins" sequence — \x1b[<top>;<bot>r
// — confines automatic scroll-on-LF to the requested region. Output
// printed past the bottom margin scrolls within the region instead of
// pushing the rest of the screen up, so the status and input rows
// stay nailed in place no matter how much the agent prints.
//
// All write paths take an io.Writer (not os.Stdout direct) so tests
// can capture the byte stream and assert on the exact escape sequences
// — this is the single source of truth for which control codes leave
// the program, and getting the wrong one wedges every terminal a user
// might try.

// Layout describes which rows belong to which band. Computed once per
// Begin from the live terminal dimensions; recomputed on SIGWINCH.
type Layout struct {
	// Rows / Cols are the full terminal dimensions in cells. 1-indexed
	// to match the way ANSI cursor-position sequences address rows.
	Rows int
	Cols int

	// ContentTop is always 1 — DECSTBM expects 1-based row numbers
	// and the content band starts at the very top of the screen.
	ContentTop int

	// ContentBottom is the last row of the scrollable content band.
	// Equals Rows-2 (reserving one row for status and one for input).
	ContentBottom int

	// StatusRow is the row dedicated to the spinner / queue
	// indicator. Equals Rows-1.
	StatusRow int

	// InputRow is the bottom-most row where the prompt and the
	// user's typing live. Equals Rows.
	InputRow int
}

// NewLayout computes the band assignment for a terminal of the given
// size. It does not validate that the size is large enough — that's
// the caller's job via ShouldActivate. Passing a too-small size
// produces a degenerate Layout (negative ContentBottom) which the
// caller can detect and bail on, rather than producing an invalid
// escape sequence that gets sent to the terminal.
func NewLayout(rows, cols int) Layout {
	return Layout{
		Rows:          rows,
		Cols:          cols,
		ContentTop:    1,
		ContentBottom: rows - 2,
		StatusRow:     rows - 1,
		InputRow:      rows,
	}
}

// Valid reports whether the layout has a positive content band. A
// degenerate layout (rows < 3) makes ContentBottom <= 0, which would
// cause DECSTBM to reject the sequence or, worse, set a weird region
// that the terminal interprets as "scroll the whole screen the wrong
// way". Callers MUST check Valid before applying a layout.
func (l Layout) Valid() bool {
	return l.Rows >= 3 && l.Cols >= 1 && l.ContentBottom >= l.ContentTop
}

// EnterRegion writes the sequence that confines scrolling to the
// content band and parks the cursor on the first content row. The
// caller is responsible for restoring the default region via
// ExitRegion on cleanup — leaving DECSTBM set is the most common
// source of "my terminal feels broken" bug reports because the
// confinement persists across processes.
//
// The sequence is split into three primitives so a future debugger
// can dump each one individually:
//
//	\x1b[<top>;<bot>r   DECSTBM — set scroll region
//	\x1b[H              CUP     — cursor to top-left (1,1)
//
// Returns the first write error; subsequent writes are skipped to
// avoid stomping a half-written sequence into the user's terminal.
func EnterRegion(w io.Writer, l Layout) error {
	if !l.Valid() {
		return fmt.Errorf("turnui: invalid layout %dx%d (content band %d..%d)",
			l.Rows, l.Cols, l.ContentTop, l.ContentBottom)
	}
	if _, err := fmt.Fprintf(w, "\x1b[%d;%dr", l.ContentTop, l.ContentBottom); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "\x1b[H"); err != nil {
		return err
	}
	return nil
}

// ExitRegion restores the terminal's default scroll region (the whole
// screen) and parks the cursor below the input row so the next prompt
// the legacy renderer prints lands on a fresh, unconfined line.
//
// Two-phase teardown matters: a plain \x1b[r reset followed by a CUP
// to the bottom is the only sequence we have seen reliably leave both
// xterm-derived emulators (iTerm2, Alacritty, kitty) AND the macOS
// Terminal.app in a sane state. Skipping either step leaves the cursor
// in the wrong band or the region clamped.
func ExitRegion(w io.Writer, l Layout) error {
	if _, err := fmt.Fprint(w, "\x1b[r"); err != nil {
		return err
	}
	// Park the cursor on the (formerly) input row so the next thing
	// printed does not stomp the status line — that row's content is
	// stale once the region is dropped, and the safest default is to
	// leave the user looking at a fresh empty line under it.
	if _, err := fmt.Fprintf(w, "\x1b[%d;1H", l.InputRow); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "\n"); err != nil {
		return err
	}
	return nil
}

// MoveCursor parks the cursor at (row, col) using the CUP sequence.
// Rows and cols are 1-based to match every other ANSI tool the user
// might compare against (tput, terminfo, the DEC manuals).
func MoveCursor(w io.Writer, row, col int) error {
	_, err := fmt.Fprintf(w, "\x1b[%d;%dH", row, col)
	return err
}

// ClearLine writes the CSI 2K sequence (erase entire line, cursor
// position unchanged). Used by the status updater before redrawing
// the spinner so stale glyphs from a longer previous message don't
// leak past the new one. The cursor stays where it was — callers that
// care about column alignment must move first, then clear.
func ClearLine(w io.Writer) error {
	_, err := fmt.Fprint(w, "\x1b[2K")
	return err
}

// SaveCursor emits ESC 7 (DEC variant of save-cursor). We use the DEC
// form rather than CSI s because some terminals — notably the GoLand
// integrated terminal that this branch's bug reports came from —
// honor one but not the other. ESC 7 has the wider support footprint
// in practice. Pair every SaveCursor with exactly one RestoreCursor
// before any further writes; nesting is not portable.
func SaveCursor(w io.Writer) error {
	_, err := fmt.Fprint(w, "\x1b7")
	return err
}

// RestoreCursor is the companion to SaveCursor (ESC 8 / DECRC).
func RestoreCursor(w io.Writer) error {
	_, err := fmt.Fprint(w, "\x1b8")
	return err
}
