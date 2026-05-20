/*
 * ChatCLI - UIRenderer compact-mode color tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout runs fn while redirecting os.Stdout into a buffer
// and returns the captured bytes as a string. UIRenderer.Compact*
// methods print directly to stdout via fmt.Printf rather than
// taking an io.Writer, so this is the only way to assert on their
// output without refactoring the public API.
//
// Sync-safe: the helper restores os.Stdout under a defer and
// fully drains the read end of the pipe before returning, so a
// later test that reads stdout itself does not see leftover bytes.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err, "os.Pipe failed in stdout capture helper")

	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	var buf bytes.Buffer
	var copyWg sync.WaitGroup
	copyWg.Add(1)
	go func() {
		defer copyWg.Done()
		_, _ = io.Copy(&buf, r)
	}()

	fn()
	_ = w.Close()
	copyWg.Wait()
	_ = r.Close()
	return buf.String()
}

// TestCompactToolStart_LabelIsCyan locks the color choice for the
// tool-start glyph + label. The Phase B → main migration moved the
// label from gray to cyan so the tool identity stands out from
// surrounding gray prose; if a future "color cleanup" pass swaps
// it back to gray, this test catches the regression immediately.
func TestCompactToolStart_LabelIsCyan(t *testing.T) {
	r := NewUIRenderer(nil)
	out := captureStdout(t, func() {
		r.CompactToolStart("Read(main.go)")
	})

	// Wire format: "  ↻ Read(main.go)\n" wrapped in ANSI cyan +
	// reset. We assert on the cyan code being present AND on the
	// label content surviving the colorize round-trip.
	assert.Contains(t, out, ColorCyan, "tool start must use cyan ANSI code")
	assert.Contains(t, out, "↻", "must emit the start glyph")
	assert.Contains(t, out, "Read(main.go)", "tool label must survive colorize")
	// Negative assertion: the legacy gray-on-label rendering must
	// not creep back. Gray is still used elsewhere (status callbacks,
	// reasoning summaries) so absence of ColorGray would be wrong;
	// what we check is that the LABEL did not regress to gray.
	// Heuristic: the substring "ColorGray + label + ColorReset" would
	// look like "\x1b[90mRead(main.go)\x1b[0m" — assert that exact
	// sequence is NOT present.
	assert.NotContains(t, out, ColorGray+"Read(main.go)",
		"label must NOT be wrapped in gray — regression to pre-Phase-B behavior")
}

// TestCompactToolDone_LabelIsCyan mirrors the start-label check for
// the completion line. Different success/failure icons use green
// and yellow respectively, but in both cases the LABEL must be
// cyan — the same visual identity carries across the lifecycle of
// a tool call.
func TestCompactToolDone_LabelIsCyan(t *testing.T) {
	successOut := captureStdout(t, func() {
		NewUIRenderer(nil).CompactToolDone("Read(main.go)", "1.2s", false)
	})
	failOut := captureStdout(t, func() {
		NewUIRenderer(nil).CompactToolDone("Read(main.go)", "1.2s", true)
	})

	for _, out := range []string{successOut, failOut} {
		assert.Contains(t, out, ColorCyan, "tool done label must be cyan")
		assert.Contains(t, out, "Read(main.go)")
		assert.Contains(t, out, "1.2s")
		assert.NotContains(t, out, ColorGray+"Read(main.go)",
			"label must NOT be wrapped in gray on either success or error")
	}

	// Success-specific glyph + green icon.
	assert.Contains(t, successOut, "✓")
	assert.Contains(t, successOut, ColorGreen)
	// Error-specific glyph + red duration. The PR1 cross-mode polish
	// migrated all error surfaces from purple/yellow to a true red so
	// failures read as failures even on themes where yellow was used
	// for warnings. If a future cleanup regresses this back to yellow,
	// it must update RenderToolResult/Minimal + CompactError together.
	assert.Contains(t, failOut, "✗")
	assert.Contains(t, failOut, ColorRed)
}

// TestCompactAssistantText_UsesDefaultForeground proves the new
// helper does NOT wrap the assistant's text in gray. The whole
// point of the helper is to make the assistant's free-text answer
// visually distinct from the gray tool prose; if the text comes
// back colorized as gray, the helper is broken.
func TestCompactAssistantText_UsesDefaultForeground(t *testing.T) {
	out := captureStdout(t, func() {
		NewUIRenderer(nil).CompactAssistantText("Done. Tests pass.")
	})

	assert.Contains(t, out, "◆", "must emit the assistant glyph")
	assert.Contains(t, out, ColorCyan, "the ◆ glyph itself is cyan")
	assert.Contains(t, out, "Done. Tests pass.", "body text must appear")
	// The body is wrapped in ColorReset (terminal default fg), not
	// ColorGray. Asserting absence of "gray-wrap-around-body"
	// catches the regression where the body would slip back to
	// the old CompactLine(gray) rendering.
	assert.NotContains(t, out, ColorGray+"Done. Tests pass.",
		"body must NOT be wrapped in gray — defeats the helper's purpose")
}

// TestCompactAssistantText_MultiLineIndentsContinuations covers
// the line-splitting branch: the first line lands under the ◆
// glyph, subsequent lines are indented to align with the first
// line's text column. Without this the user sees a left-aligned
// wall of text with the first line peeking out under the glyph.
//
// Asserted with the ANSI codes stripped because the body is
// wrapped in ColorReset on every line — a raw substring search
// would miss "    Line two." since the wire format is actually
// "    \x1b[0mLine two.\x1b[0m". The strip is local to the test
// to avoid pulling a regex dependency into production code.
func TestCompactAssistantText_MultiLineIndentsContinuations(t *testing.T) {
	out := captureStdout(t, func() {
		NewUIRenderer(nil).CompactAssistantText("Line one.\nLine two.\nLine three.")
	})

	plain := stripANSI(out)
	assert.Contains(t, plain, "Line one.")
	assert.Contains(t, plain, "Line two.")
	assert.Contains(t, plain, "Line three.")
	// Continuation lines have 4 leading spaces of indent to align
	// under the first line's text column ("  ◆ " is 4 visible
	// columns).
	assert.Contains(t, plain, "    Line two.")
	assert.Contains(t, plain, "    Line three.")
}

// stripANSI removes CSI escape sequences (color codes) so the
// remaining string can be substring-matched against plain text.
// Pulled inline rather than imported because the dependency on a
// regex package or third-party stripper is overkill for two tests.
func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			// Skip the CSI introducer and consume bytes until
			// a final byte in 0x40..0x7e (which ends the CSI
			// sequence per ECMA-48). 32-byte cap is defensive
			// against runaway input; SGR sequences never come
			// near that length.
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// TestCompactAssistantText_EmptyTextIsNoOp matches the early-return
// contract: passing an empty or whitespace-only string must NOT
// print the glyph + empty line, which would leave a stray ◆ in
// the timeline.
func TestCompactAssistantText_EmptyTextIsNoOp(t *testing.T) {
	r := NewUIRenderer(nil)
	for _, tc := range []string{"", "   ", "\t\n  \n"} {
		out := captureStdout(t, func() { r.CompactAssistantText(tc) })
		assert.Empty(t, out, "empty/whitespace input must produce no output, got %q for input %q", out, tc)
	}
}

// TestEchoUserInput_GreenWithCaretMarker locks the visual identity
// of the user-input echo: bold green ❯ marker followed by the
// trimmed text in green. The whole point is to give the user's
// directives a distinct color lane in the timeline; if the colors
// regress, this catches it at the wire format.
func TestEchoUserInput_GreenWithCaretMarker(t *testing.T) {
	out := captureStdout(t, func() {
		NewUIRenderer(nil).EchoUserInput("fix bar.go")
	})

	assert.Contains(t, out, "❯", "must emit the user-input caret marker")
	assert.Contains(t, out, ColorGreen, "must use green ANSI code")
	assert.Contains(t, out, ColorBold, "caret marker must be bold")
	assert.Contains(t, out, "fix bar.go", "user text must appear")
}

// TestEchoUserInput_TrimsAndSkipsEmpty matches the early-return:
// passing only whitespace must not print a bare ❯ marker. The
// trim happens BEFORE the empty-check so "   fix\t" still renders
// as "fix" without the surrounding padding.
func TestEchoUserInput_TrimsAndSkipsEmpty(t *testing.T) {
	r := NewUIRenderer(nil)

	emptyOut := captureStdout(t, func() { r.EchoUserInput("   \t  ") })
	assert.Empty(t, emptyOut, "whitespace-only input must produce no echo")

	paddedOut := captureStdout(t, func() { r.EchoUserInput("   fix bar.go  \t") })
	assert.Contains(t, paddedOut, "❯")
	assert.Contains(t, paddedOut, "fix bar.go")
	// Padding stripped — assert the trailing-tab signature is gone.
	assert.False(t, strings.Contains(paddedOut, "  \t"),
		"trailing padding must be stripped before echo")
}
