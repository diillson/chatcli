/*
 * ChatCLI - Coder turn-UI mini readline tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package turnui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePainter records every paint call so tests can assert on the
// echoed buffer contents without juggling escape sequences. It
// satisfies the inputPainter interface that RunReadLine consumes.
type fakePainter struct {
	mu     sync.Mutex
	frames []string
}

func (p *fakePainter) PaintInput(buf *LineBuffer) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.frames = append(p.frames, buf.String())
	return nil
}

func (p *fakePainter) framesSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.frames))
	copy(out, p.frames)
	return out
}

// scriptedReader hands out predetermined byte chunks and then
// signals EOF. Used to feed the input loop a fixed sequence in
// tests without needing an actual TTY.
type scriptedReader struct {
	chunks [][]byte
	idx    int
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[r.idx])
	r.idx++
	return n, nil
}

// TestApplyKey_TableOfBehavior is the per-key contract: KeyChar
// appends + repaints, KeyEnter submits, KeyBackspace deletes,
// KeyCtrlC cancels, etc. Each row is one keystroke; the verdict
// columns mirror applyKey's return tuple. A change in any cell
// is a UX change that warrants a code review.
func TestApplyKey_TableOfBehavior(t *testing.T) {
	tests := []struct {
		name       string
		seed       string
		key        Key
		wantBuf    string
		wantSubmit bool
		wantExit   exitKind
		wantPaint  bool
	}{
		{"char appends and repaints", "abc", Key{KeyChar, 'd'}, "abcd", false, exitNone, true},
		{"backspace deletes and repaints", "abc", Key{KeyBackspace, 0}, "ab", false, exitNone, true},
		{"backspace on empty is no-op", "", Key{KeyBackspace, 0}, "", false, exitNone, false},
		{"enter submits without repaint", "abc", Key{KeyEnter, 0}, "abc", true, exitNone, false},
		{"ctrl+c cancels", "abc", Key{KeyCtrlC, 0}, "abc", false, exitCancel, false},
		{"ctrl+d empty exits", "", Key{KeyCtrlD, 0}, "", false, exitEOF, false},
		{"ctrl+d non-empty is ignored", "abc", Key{KeyCtrlD, 0}, "abc", false, exitNone, false},
		{"ctrl+u kills line", "the line", Key{KeyCtrlU, 0}, "", false, exitNone, true},
		{"ctrl+w kills word", "the line ", Key{KeyCtrlW, 0}, "the ", false, exitNone, true},
		{"unknown key is ignored", "abc", Key{KeyUnknown, 0}, "abc", false, exitNone, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := NewLineBuffer()
			for _, r := range tc.seed {
				buf.Append(r)
			}
			submit, exit, repaint := applyKey(tc.key, buf)
			assert.Equal(t, tc.wantBuf, buf.String())
			assert.Equal(t, tc.wantSubmit, submit)
			assert.Equal(t, tc.wantExit, exit)
			assert.Equal(t, tc.wantPaint, repaint)
		})
	}
}

// TestRunReadLine_SubmitsTrimmedLineOnEnter walks the loop end-to-end
// with a scripted sequence: "hi\r". Asserts the OnSubmit callback
// fires with the trimmed contents, the buffer is reset, and the
// frames show the typing progression.
func TestRunReadLine_SubmitsTrimmedLineOnEnter(t *testing.T) {
	painter := &fakePainter{}
	var submitted string
	var wg sync.WaitGroup
	wg.Add(1)

	reader := &scriptedReader{chunks: [][]byte{
		[]byte("hi"),
		{0x0d}, // Enter
	}}

	cfg := ReadLineConfig{
		Reader:  reader,
		Painter: painter,
		OnSubmit: func(line string) {
			submitted = line
			wg.Done()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- RunReadLine(ctx, cfg) }()

	wg.Wait()
	cancel()
	err := <-errCh
	require.NoError(t, err)

	assert.Equal(t, "hi", submitted)
	// First frame is the initial empty paint; later frames show
	// progressive append plus the post-submit reset back to empty.
	assert.Contains(t, painter.framesSnapshot(), "hi")
	assert.Equal(t, "", painter.framesSnapshot()[len(painter.framesSnapshot())-1],
		"after Enter the buffer is reset and the row repaints empty")
}

// TestRunReadLine_BackspaceErasesGlyph reproduces the live workflow
// of typing a multi-byte character and erasing it. Without rune
// awareness this would leave a dangling 0xC3 in the buffer and the
// next paint would emit mojibake. The submitted line is checked to
// confirm the buffer is what the user sees.
func TestRunReadLine_BackspaceErasesGlyph(t *testing.T) {
	painter := &fakePainter{}
	var submitted string
	var wg sync.WaitGroup
	wg.Add(1)

	reader := &scriptedReader{chunks: [][]byte{
		[]byte("olá"),
		{0x7f}, // Backspace (DEL)
		{0x0d}, // Enter
	}}

	cfg := ReadLineConfig{
		Reader:  reader,
		Painter: painter,
		OnSubmit: func(line string) {
			submitted = line
			wg.Done()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- RunReadLine(ctx, cfg) }()

	wg.Wait()
	cancel()
	require.NoError(t, <-errCh)
	assert.Equal(t, "ol", submitted, "Backspace must delete the full 'á' glyph")
}

// TestRunReadLine_CancelFiresOnCtrlC checks that Ctrl+C produces a
// cancel event without aborting the loop. The agent uses cancel to
// abort the current LLM turn; the input row should immediately be
// ready for the next message.
func TestRunReadLine_CancelFiresOnCtrlC(t *testing.T) {
	painter := &fakePainter{}
	cancelled := false
	var mu sync.Mutex
	cond := sync.NewCond(&mu)

	reader := &scriptedReader{chunks: [][]byte{
		[]byte("abc"),
		{0x03}, // Ctrl+C
	}}

	cfg := ReadLineConfig{
		Reader:  reader,
		Painter: painter,
		OnCancel: func() {
			mu.Lock()
			cancelled = true
			cond.Signal()
			mu.Unlock()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- RunReadLine(ctx, cfg) }()

	mu.Lock()
	for !cancelled {
		cond.Wait()
	}
	mu.Unlock()
	cancel()
	require.NoError(t, <-errCh)

	assert.True(t, cancelled)
	frames := painter.framesSnapshot()
	assert.Equal(t, "", frames[len(frames)-1], "after Ctrl+C the buffer is reset")
}

// TestRunReadLine_EOFOnEmptyExits matches the shell convention:
// Ctrl+D on an empty buffer ends the input loop cleanly. The agent
// translates this into "user is done with /coder for this session".
func TestRunReadLine_EOFOnEmptyExits(t *testing.T) {
	painter := &fakePainter{}
	reader := &scriptedReader{chunks: [][]byte{
		{0x04}, // Ctrl+D on empty
	}}

	cfg := ReadLineConfig{Reader: reader, Painter: painter}
	require.NoError(t, RunReadLine(context.Background(), cfg))
}

// TestRunReadLine_SplitMultiByteAcrossReads reproduces the boundary
// case where a UTF-8 sequence is split across two reads (TTY may
// flush after the lead byte; rare but possible). Without carry, the
// loop would treat each half as an invalid byte and drop both.
func TestRunReadLine_SplitMultiByteAcrossReads(t *testing.T) {
	painter := &fakePainter{}
	var submitted string
	var wg sync.WaitGroup
	wg.Add(1)

	// "ç" is 0xC3 0xA7; split into two reads.
	reader := &scriptedReader{chunks: [][]byte{
		{0xc3},
		{0xa7},
		{0x0d}, // Enter
	}}

	cfg := ReadLineConfig{
		Reader:  reader,
		Painter: painter,
		OnSubmit: func(line string) {
			submitted = line
			wg.Done()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- RunReadLine(ctx, cfg) }()

	wg.Wait()
	cancel()
	require.NoError(t, <-errCh)
	assert.Equal(t, "ç", submitted, "multi-byte rune split across reads must be reassembled")
}

// TestRunReadLine_RejectsMissingDeps catches the configuration
// mistake of forgetting to wire the Painter or Reader. The error is
// returned early so the caller can fall back without leaving the
// terminal in a half-initialized state.
func TestRunReadLine_RejectsMissingDeps(t *testing.T) {
	err := RunReadLine(context.Background(), ReadLineConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Reader and Painter")
}

// TestPaintInput_DrawsPromptOnInputRowAndRecordsCursor asserts the
// live painter (TurnUI.PaintInput) writes the expected sequence:
// move to input row, clear it, write prompt + buffer, then move the
// cursor to the buffer's logical insertion column AND record that
// column in lastInputCol so UpdateStatus can snap back to it later
// without depending on ESC 7 / 8 save/restore.
func TestPaintInput_DrawsPromptOnInputRowAndRecordsCursor(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.Begin(24, 80))
	buf.Reset()

	lb := NewLineBuffer()
	for _, r := range "fix bar.go" {
		lb.Append(r)
	}
	require.NoError(t, u.PaintInput(lb))

	got := buf.String()
	assert.Contains(t, got, "\x1b[24;1H", "moved to input row (24,1)")
	assert.Contains(t, got, "\x1b[2K", "cleared the row")
	assert.Contains(t, got, InputPrompt+"fix bar.go", "prompt + buffer contents written")
	assert.NotContains(t, got, "\x1b7", "input paint must NOT save the cursor")
	assert.NotContains(t, got, "\x1b8", "input paint must NOT restore the cursor")

	// "❯ " is 2 visible columns; buffer "fix bar.go" is 10
	// runes; final cursor column is 2 + 10 + 1 = 13 (1-based
	// past the last glyph, where the next keystroke inserts).
	assert.Equal(t, int32(13), u.lastInputCol.Load(),
		"lastInputCol must reflect promptWidth + cursorOffset + 1 so UpdateStatus can restore the cursor")
}

// TestPaintInput_NoOpWhenInactive matches paintStatus's behavior.
// The reader loop can fire PaintInput from its own goroutine after
// End has fired without producing stray writes to the terminal.
func TestPaintInput_NoOpWhenInactive(t *testing.T) {
	var buf bytes.Buffer
	u := New(&buf)
	require.NoError(t, u.PaintInput(NewLineBuffer()))
	assert.Empty(t, buf.String())
}

// TestRunReadLine_PaintErrorBubblesUp ensures a paint failure is
// surfaced to the caller rather than being swallowed. The caller
// needs to know so it can restore the TTY before reporting the
// error — silent paint failures are how terminals end up wedged.
func TestRunReadLine_PaintErrorBubblesUp(t *testing.T) {
	reader := strings.NewReader("a")
	cfg := ReadLineConfig{
		Reader:  reader,
		Painter: &erroringPainter{},
	}
	err := RunReadLine(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initial input paint")
}

type erroringPainter struct{}

func (e *erroringPainter) PaintInput(_ *LineBuffer) error {
	return io.ErrShortWrite
}

// TestApplyKey_NavigationKeysMutateCursor covers the keys Phase G
// added: ←/→/Home/End/Ctrl+A/Ctrl+E move the buffer cursor, Delete
// removes forward. Each behaves as a no-op when the cursor cannot
// move (start/end of line) — the bool return is the paint signal.
func TestApplyKey_NavigationKeysMutateCursor(t *testing.T) {
	tests := []struct {
		name      string
		seed      string
		startPos  int
		key       Key
		wantBuf   string
		wantCur   int
		wantPaint bool
	}{
		{"left moves cursor back", "abc", 3, Key{KeyArrowLeft, 0}, "abc", 2, true},
		{"left no-op at col 0", "abc", 0, Key{KeyArrowLeft, 0}, "abc", 0, false},
		{"right moves cursor forward", "abc", 0, Key{KeyArrowRight, 0}, "abc", 1, true},
		{"right no-op at end", "abc", 3, Key{KeyArrowRight, 0}, "abc", 3, false},
		{"home jumps to start", "abc", 3, Key{KeyHome, 0}, "abc", 0, true},
		{"ctrl+a jumps to start", "abc", 3, Key{KeyCtrlA, 0}, "abc", 0, true},
		{"end jumps to end", "abc", 0, Key{KeyEnd, 0}, "abc", 3, true},
		{"ctrl+e jumps to end", "abc", 0, Key{KeyCtrlE, 0}, "abc", 3, true},
		{"delete removes at cursor", "abc", 1, Key{KeyDelete, 0}, "ac", 1, true},
		{"delete no-op at end", "abc", 3, Key{KeyDelete, 0}, "abc", 3, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := NewLineBuffer()
			for _, r := range tc.seed {
				buf.Append(r)
			}
			// Move cursor to the start position via MoveStart +
			// N×MoveRight so the test does not depend on internal
			// representation.
			buf.MoveStart()
			for i := 0; i < tc.startPos; i++ {
				buf.MoveRight()
			}
			_, _, repaint := applyKey(tc.key, buf)
			assert.Equal(t, tc.wantBuf, buf.String())
			assert.Equal(t, tc.wantCur, buf.Cursor())
			assert.Equal(t, tc.wantPaint, repaint)
		})
	}
}

// TestRunReadLine_HistoryUpAndDown wires the History through the
// loop and exercises ↑↓ navigation end-to-end. The scripted reader
// submits "first" and "second", then ↑ recalls "second", ↑ recalls
// "first", ↓ goes back to "second", ↓ restores the empty draft.
// The frame snapshot captures the buffer contents after each paint.
func TestRunReadLine_HistoryUpAndDown(t *testing.T) {
	painter := &fakePainter{}
	hist := NewHistory(10)
	hist.Append("first")
	hist.Append("second")

	var submissions []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)

	reader := &scriptedReader{chunks: [][]byte{
		{0x1b, '[', 'A'},                                          // ↑ → "second"
		{0x1b, '[', 'A'},                                          // ↑ → "first"
		{0x1b, '[', 'B'},                                          // ↓ → "second"
		{0x1b, '[', 'B'},                                          // ↓ → restore empty draft
		[]byte("hi"),                                              // type
		{0x0d},                                                    // Enter
	}}

	cfg := ReadLineConfig{
		Reader:  reader,
		Painter: painter,
		History: hist,
		OnSubmit: func(line string) {
			mu.Lock()
			submissions = append(submissions, line)
			mu.Unlock()
			wg.Done()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- RunReadLine(ctx, cfg) }()
	wg.Wait()
	cancel()
	require.NoError(t, <-errCh)

	assert.Equal(t, []string{"hi"}, submissions)

	// History should now contain first, second, hi (the most
	// recent submission was appended).
	assert.Equal(t, 3, hist.Len())

	// Frames should include the recalled entries.
	frames := painter.framesSnapshot()
	assert.Contains(t, frames, "second")
	assert.Contains(t, frames, "first")
	assert.Contains(t, frames, "hi")
}

// TestRunReadLine_ArrowsMoveCursorInBuffer is the integration test
// for inline navigation. Type "ad", ←, insert "bc", Enter →
// submission is "abcd" (not "adbc" or "bcad"). Without the cursor-
// aware Insert path the test fails with the latter strings.
func TestRunReadLine_ArrowsMoveCursorInBuffer(t *testing.T) {
	painter := &fakePainter{}
	var submitted string
	var wg sync.WaitGroup
	wg.Add(1)

	reader := &scriptedReader{chunks: [][]byte{
		[]byte("ad"),
		{0x1b, '[', 'D'}, // ←
		[]byte("bc"),
		{0x0d}, // Enter
	}}

	cfg := ReadLineConfig{
		Reader:  reader,
		Painter: painter,
		OnSubmit: func(line string) {
			submitted = line
			wg.Done()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- RunReadLine(ctx, cfg) }()
	wg.Wait()
	cancel()
	require.NoError(t, <-errCh)

	assert.Equal(t, "abcd", submitted, "← + Insert places chars at cursor, not at end")
}
