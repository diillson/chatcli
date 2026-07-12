/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	prompt "github.com/c-bata/go-prompt"
	runewidth "github.com/mattn/go-runewidth"
)

// wrapAwareWriter aligns a deferred-EOL-wrap console with go-prompt's Windows
// renderer, which assumes legacy immediate wrap.
//
// go-prompt models the cursor as y = cells/width (render.go toPos) and, on
// Unix, materializes the wrap pending at an exact column boundary by emitting
// "\n" (render.go lineWrap). On Windows that "\n" is skipped on the assumption
// that the legacy conhost wraps immediately — but with VT processing enabled
// (Windows Terminal, or conhost after EnableVirtualTerminal) the console uses
// xterm-style DEFERRED wrap, so the software model drifts one row for every
// render that ends exactly on a column boundary. The renderer's CursorUp then
// overshoots and the prompt climbs, leaving ghost copies of the input line —
// worst with the animated live prefix re-rendering ~4×/s and with the
// completion dropdown (whose rows go through the same lineWrap).
//
// This wrapper re-introduces the newline at the writer level: it tracks the
// cursor column across writer calls and, whenever a text write lands exactly
// on the terminal's right edge, emits the "\n" that materializes the wrap —
// making the console behave the way go-prompt's Windows math expects.
type wrapAwareWriter struct {
	prompt.ConsoleWriter
	width func() int // terminal width in cells; re-queried on every Flush
	w     int        // cached width for the current render pass (0 = passthrough)
	col   int        // tracked cursor column, 0-based
}

// newWrapAwareWriter wraps inner. width is consulted at construction and on
// every Flush (one render pass = one flush); returning 0 disables injection.
func newWrapAwareWriter(inner prompt.ConsoleWriter, width func() int) *wrapAwareWriter {
	return &wrapAwareWriter{
		ConsoleWriter: inner,
		width:         width,
		w:             width(),
	}
}

// writeCells scans text, tracking the column and flushing through emit; when
// a rune lands exactly on the right edge it emits the pending segment plus a
// raw "\n" so the terminal wraps NOW, exactly as go-prompt's model assumes.
func (w *wrapAwareWriter) writeCells(text string, emit func(string)) {
	if w.w <= 0 {
		emit(text)
		return
	}
	start := 0
	for i, r := range text {
		if r == '\n' || r == '\r' {
			w.col = 0
			continue
		}
		w.col += runewidth.RuneWidth(r)
		if w.col >= w.w {
			end := i + len(string(r))
			emit(text[start:end])
			w.ConsoleWriter.WriteRawStr("\n")
			w.col = 0
			start = end
		}
	}
	if start < len(text) {
		emit(text[start:])
	}
}

func (w *wrapAwareWriter) WriteStr(data string) {
	w.writeCells(data, w.ConsoleWriter.WriteStr)
}

func (w *wrapAwareWriter) Write(data []byte) {
	w.writeCells(string(data), func(s string) { w.ConsoleWriter.Write([]byte(s)) })
}

// WriteRawStr carries go-prompt's own control sequences (colors, erases,
// moves); nothing to inject, but the column must stay tracked.
func (w *wrapAwareWriter) WriteRawStr(data string) {
	w.trackRaw(data)
	w.ConsoleWriter.WriteRawStr(data)
}

func (w *wrapAwareWriter) WriteRaw(data []byte) {
	w.trackRaw(string(data))
	w.ConsoleWriter.WriteRaw(data)
}

// trackRaw advances the column model over a raw byte stream, skipping ANSI
// escape sequences (which do not print) and resetting on \r / \n. Cursor
// movement is not expected through raw writes — go-prompt uses the dedicated
// Cursor* methods, which are tracked below.
func (w *wrapAwareWriter) trackRaw(data string) {
	if w.w <= 0 {
		return
	}
	inEsc := false
	for _, r := range data {
		switch {
		case inEsc:
			// CSI sequences end on a final byte in @-~ (0x40-0x7e); the
			// bracket and parameter bytes fall below that range.
			if r >= 0x40 && r <= 0x7e && r != '[' {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		case r == '\n' || r == '\r':
			w.col = 0
		default:
			w.col += runewidth.RuneWidth(r)
			if w.col >= w.w {
				// Raw text is not wrap-compensated (only tracked); model the
				// deferred state as "at the edge" so the next tracked write
				// resolves it.
				w.col = 0
			}
		}
	}
}

func (w *wrapAwareWriter) Flush() error {
	err := w.ConsoleWriter.Flush()
	if next := w.width(); next > 0 {
		w.w = next
	}
	return err
}

func (w *wrapAwareWriter) CursorGoTo(row, col int) {
	if col <= 1 {
		w.col = 0
	} else {
		w.col = col - 1 // ANSI CUP is 1-based
	}
	w.ConsoleWriter.CursorGoTo(row, col)
}

func (w *wrapAwareWriter) CursorForward(n int) {
	if n > 0 {
		w.col += n
		if w.w > 0 && w.col > w.w-1 {
			w.col = w.w - 1 // terminals clamp at the right edge
		}
	}
	w.ConsoleWriter.CursorForward(n)
}

func (w *wrapAwareWriter) CursorBackward(n int) {
	if n > 0 {
		w.col -= n
		if w.col < 0 {
			w.col = 0
		}
	}
	w.ConsoleWriter.CursorBackward(n)
}
