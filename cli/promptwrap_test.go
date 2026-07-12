/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	prompt "github.com/c-bata/go-prompt"
)

// recordingWriter captures every call so tests can assert exactly what the
// wrapper forwards, including injected raw newlines.
type recordingWriter struct {
	log []string
}

func (r *recordingWriter) WriteRaw(data []byte)    { r.log = append(r.log, "raw:"+string(data)) }
func (r *recordingWriter) Write(data []byte)       { r.log = append(r.log, "str:"+string(data)) }
func (r *recordingWriter) WriteRawStr(data string) { r.log = append(r.log, "raw:"+data) }
func (r *recordingWriter) WriteStr(data string)    { r.log = append(r.log, "str:"+data) }
func (r *recordingWriter) Flush() error            { return nil }
func (r *recordingWriter) EraseScreen()            {}
func (r *recordingWriter) EraseUp()                {}
func (r *recordingWriter) EraseDown()              {}
func (r *recordingWriter) EraseStartOfLine()       {}
func (r *recordingWriter) EraseEndOfLine()         {}
func (r *recordingWriter) EraseLine()              {}
func (r *recordingWriter) ShowCursor()             {}
func (r *recordingWriter) HideCursor()             {}
func (r *recordingWriter) CursorGoTo(row, col int) {}
func (r *recordingWriter) CursorUp(n int)          {}
func (r *recordingWriter) CursorDown(n int)        {}
func (r *recordingWriter) CursorForward(n int)     {}
func (r *recordingWriter) CursorBackward(n int)    {}
func (r *recordingWriter) AskForCPR()              {}
func (r *recordingWriter) SaveCursor()             {}
func (r *recordingWriter) UnSaveCursor()           {}
func (r *recordingWriter) ScrollDown()             {}
func (r *recordingWriter) ScrollUp()               {}
func (r *recordingWriter) SetTitle(title string)   {}
func (r *recordingWriter) ClearTitle()             {}
func (r *recordingWriter) SetColor(fg, bg prompt.Color, bold bool) {
	r.log = append(r.log, "color")
}

func (r *recordingWriter) joined() string { return strings.Join(r.log, "|") }

var _ prompt.ConsoleWriter = (*recordingWriter)(nil)

func newTestWrapWriter(width int) (*wrapAwareWriter, *recordingWriter) {
	rec := &recordingWriter{}
	return newWrapAwareWriter(rec, func() int { return width }), rec
}

func TestWrapAwareWriter_InjectsNewlineAtExactBoundary(t *testing.T) {
	w, rec := newTestWrapWriter(10)
	w.WriteStr("0123456789")
	want := "str:0123456789|raw:\n"
	if got := rec.joined(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if w.col != 0 {
		t.Errorf("col = %d, want 0 after wrap", w.col)
	}
}

func TestWrapAwareWriter_NoInjectionShortOfBoundary(t *testing.T) {
	w, rec := newTestWrapWriter(10)
	w.WriteStr("012345678")
	if got := rec.joined(); got != "str:012345678" {
		t.Errorf("got %q, want no injected newline", got)
	}
	if w.col != 9 {
		t.Errorf("col = %d, want 9", w.col)
	}
}

func TestWrapAwareWriter_AccumulatesAcrossWrites(t *testing.T) {
	w, rec := newTestWrapWriter(10)
	w.WriteStr("01234")
	w.WriteStr("56789xy")
	// The boundary falls mid-second-write: segment up to "9", newline, rest.
	want := "str:01234|str:56789|raw:\n|str:xy"
	if got := rec.joined(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if w.col != 2 {
		t.Errorf("col = %d, want 2", w.col)
	}
}

func TestWrapAwareWriter_WideRunes(t *testing.T) {
	w, rec := newTestWrapWriter(10)
	w.WriteStr("ああああああ") // 6 runes × 2 cells = 12 → boundary after the 5th
	want := "str:あああああ|raw:\n|str:あ"
	if got := rec.joined(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWrapAwareWriter_NewlineResetsColumn(t *testing.T) {
	w, rec := newTestWrapWriter(10)
	w.WriteStr("01234\r\n0123456789")
	// Explicit newline resets the column; the second line hits the boundary.
	want := "str:01234\r\n0123456789|raw:\n"
	if got := rec.joined(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWrapAwareWriter_CursorMovesAdjustColumn(t *testing.T) {
	w, rec := newTestWrapWriter(10)
	w.WriteStr("01234567") // col 8
	w.CursorBackward(3)    // col 5
	w.WriteStr("56789")    // hits boundary at 10
	want := "str:01234567|str:56789|raw:\n"
	if got := rec.joined(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	w2, _ := newTestWrapWriter(10)
	w2.WriteStr("0123")
	w2.CursorGoTo(0, 0)
	if w2.col != 0 {
		t.Errorf("col after CursorGoTo home = %d, want 0", w2.col)
	}
}

func TestWrapAwareWriter_RawEscapesDoNotCount(t *testing.T) {
	w, _ := newTestWrapWriter(10)
	w.WriteRawStr("\x1b[32m") // color: zero cells
	if w.col != 0 {
		t.Errorf("col after SGR = %d, want 0", w.col)
	}
	w.WriteRawStr("ab\x1b[0mc")
	if w.col != 3 {
		t.Errorf("col after mixed raw = %d, want 3", w.col)
	}
}

func TestWrapAwareWriter_ZeroWidthPassthrough(t *testing.T) {
	w, rec := newTestWrapWriter(0)
	w.WriteStr("0123456789012345")
	if got := rec.joined(); got != "str:0123456789012345" {
		t.Errorf("got %q, want untouched passthrough", got)
	}
}

func TestWrapAwareWriter_FlushRefreshesWidth(t *testing.T) {
	width := 10
	rec := &recordingWriter{}
	w := newWrapAwareWriter(rec, func() int { return width })
	width = 5
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	w.WriteStr("01234")
	want := "str:01234|raw:\n"
	if got := rec.joined(); got != want {
		t.Errorf("got %q, want %q (width must refresh on Flush)", got, want)
	}
}
