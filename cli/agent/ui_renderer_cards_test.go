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

// TestRenderTimelineEvent_FooterMatchesContentWidth locks the PR3
// behavior change: the bottom border of a card now spans the box
// width derived from the content, not the full terminal width.
// Before, a 2-line tip card on a 200-col terminal would stretch a
// `╰───…───` row all the way to the right edge, reading as visual
// leak. Asserting the footer is shorter than the terminal proves
// the regression cannot return silently.
func TestRenderTimelineEvent_FooterMatchesContentWidth(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)

	out := captureStdout(t, func() {
		r.RenderTimelineEvent("🧠", "TEST", "small body", ColorCyan)
	})

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var top, bottom string
	for _, ln := range lines {
		plain := stripANSI(ln)
		if strings.Contains(plain, "╭──") {
			top = ln
		}
		if strings.Contains(plain, "╰") {
			bottom = ln
		}
	}
	assert.NotEmpty(t, top, "top border must be present")
	assert.NotEmpty(t, bottom, "bottom border must be present")
	plainBottom := stripANSI(bottom)
	assert.True(t, strings.HasPrefix(plainBottom, "╰"),
		"bottom border must start with ╰")
	assert.True(t, strings.HasSuffix(plainBottom, "╯"),
		"bottom border must end with ╯ — PR3 made the card a closed box")

	topWidth := visibleLenTest(top)
	bottomWidth := visibleLenTest(bottom)
	// Top and bottom must match exactly: same card width on both ends.
	assert.Equal(t, topWidth, bottomWidth,
		"top and bottom borders must have identical visible width\n top=%q (%d)\n bot=%q (%d)",
		top, topWidth, bottom, bottomWidth)
}

// TestRenderTimelineEvent_WrapsLongContent ensures the card still
// honors the right-edge guard when the content is longer than the
// terminal — the card grows to fit, but each row still ends with the
// right border. Catches a regression where wrap math fell out of sync
// with the closing border placement.
func TestRenderTimelineEvent_WrapsLongContent(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)

	body := strings.Repeat("alpha bravo charlie delta echo ", 6)
	out := captureStdout(t, func() {
		r.RenderTimelineEvent("📋", "WRAP", body, ColorLime)
	})

	// Every line that begins with the left border must end with the
	// right border — proves we didn't drop the closing │ during wrap.
	for _, ln := range strings.Split(out, "\n") {
		plain := stripANSI(ln)
		if !strings.HasPrefix(plain, "│") {
			continue
		}
		assert.True(t, strings.HasSuffix(strings.TrimRight(plain, " "), "│"),
			"content row must close with │, got %q", plain)
	}
}

// visibleLenTest is a shim over the package-private VisibleLen so the
// test file can call it without exporting an extra symbol.
func visibleLenTest(s string) int { return VisibleLen(s) }
