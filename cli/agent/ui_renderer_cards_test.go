/*
 * ChatCLI - UIRenderer card geometry tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRenderTimelineEvent_SobrioShape locks the borderless card: a bold
// title line on the two-space indent, body on the four-space indent, and
// no frame glyph anywhere.
func TestRenderTimelineEvent_SobrioShape(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)

	out := captureStdout(t, func() {
		r.RenderTimelineEvent("🧠", "TEST", "small body", ColorCyan)
	})
	plain := stripANSI(out)

	assert.Contains(t, plain, "  🧠 TEST", "title line sits on the two-space indent")
	assert.Contains(t, plain, "    small body", "body sits on the four-space indent")
	for _, glyph := range []string{"╭", "╰", "│", "╮", "╯"} {
		assert.NotContains(t, plain, glyph, "sóbrio card draws no frame")
	}
}

// TestRenderTimelineEvent_WrapsLongContent ensures long content wraps to
// the inner width on the body indent — no row escapes the content width.
func TestRenderTimelineEvent_WrapsLongContent(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)

	body := strings.Repeat("alpha bravo charlie delta echo ", 6)
	out := captureStdout(t, func() {
		r.RenderTimelineEvent("📋", "WRAP", body, ColorLime)
	})

	for _, ln := range strings.Split(out, "\n") {
		plain := stripANSI(ln)
		assert.LessOrEqual(t, visibleLenTest(plain), 100,
			"wrapped row must stay inside the fallback content width: %q", plain)
		if strings.TrimSpace(plain) != "" && !strings.Contains(plain, "WRAP") {
			assert.True(t, strings.HasPrefix(plain, "    "),
				"body rows sit on the four-space indent: %q", plain)
		}
	}
}

// visibleLenTest is a shim over the package-private VisibleLen so the
// test file can call it without exporting an extra symbol.
func visibleLenTest(s string) int { return VisibleLen(s) }

// TestRenderTimelineEvent_TrimsLeadingTrailingBlanks: glamour bodies often
// arrive with bookend newlines; the card must open and close on real text.
func TestRenderTimelineEvent_TrimsLeadingTrailingBlanks(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)

	body := "\n\n  Olá! Tudo certo? Como posso ajudar?  \n\n"
	out := captureStdout(t, func() {
		r.RenderTimelineEvent("💬", "RESPOSTA", body, ColorGray)
	})

	lines := strings.Split(strings.TrimRight(stripANSI(out), "\n"), "\n")
	// Last line must be the body text, not a trailing blank.
	assert.Contains(t, lines[len(lines)-1], "Como posso ajudar?",
		"card must close on real content — trailing blank survived the trim")
}

// TestTrimBlankBorderRows_PreservesMiddleBlanks proves the helper
// only touches the edges. A paragraph break the user wrote in
// markdown lives as a blank line in wrapText output; that intentional
// break must NOT be eaten by the trim or we destroy the author's
// formatting decision.
func TestTrimBlankBorderRows_PreservesMiddleBlanks(t *testing.T) {
	got := trimBlankBorderRows([]string{"", "", "head", "", "", "tail", "", ""})
	want := []string{"head", "", "", "tail"}
	assert.Equal(t, want, got)
}

// TestTrimBlankBorderRows_NoOpWhenClean is a fast-path check: when
// there's nothing to trim, the helper must return the input slice
// unchanged (same address would be ideal, but Go slice identity is
// brittle to test — value equality is the practical contract).
func TestTrimBlankBorderRows_NoOpWhenClean(t *testing.T) {
	input := []string{"alpha", "beta", "gamma"}
	got := trimBlankBorderRows(input)
	assert.Equal(t, input, got)
}
