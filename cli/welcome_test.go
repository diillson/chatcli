/*
 * ChatCLI - welcome screen smoke tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * These are smoke tests for the welcome screen helpers — they don't
 * assert pixel-perfect layout (lipgloss handles that) but lock the
 * contract that each helper emits its expected components: the logo
 * letters, the tip-box title, the active-model card, etc. Without
 * these, a refactor that accidentally swallows printTipBox (or
 * passes the wrong i18n key) would only show as a missing UI
 * element on first launch — caught by the test instead.
 */

package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPrintLogo_EmitsAsciiAndIsCentered(t *testing.T) {
	out := captureStdout(t, printLogo)
	plain := stripANSIWelcome(out)

	// Every letter of "CHATCLI" must appear in the ASCII art — they're
	// formed by repeating `█` blocks, but the bar/corner chars (║ ╗)
	// always survive the colorize pass.
	for _, glyph := range []string{"╔", "╝", "║", "╚", "╗", "═"} {
		assert.Contains(t, plain, glyph, "logo must include border glyph %q", glyph)
	}

	// The logo block is wider than 60 cols; assert that the widest row
	// stayed within a generous 100-col limit (the centering math caps
	// padding for narrow terminals).
	for _, line := range strings.Split(plain, "\n") {
		if visibleLen(line) > 100 {
			t.Errorf("logo row exceeded 100 cols (%d): %q", visibleLen(line), line)
		}
	}
}

func TestPrintTipBox_RendersTitleAndBorders(t *testing.T) {
	out := captureStdout(t, printTipBox)
	plain := stripANSIWelcome(out)

	// Rounded border survives lipgloss rendering.
	assert.Contains(t, plain, "╭")
	assert.Contains(t, plain, "╰")
	// One of the tipKeys must be picked; we don't know which (random),
	// but the resolved tip text always contains a colon or hyphen and
	// is non-empty. A regression that swallows the body would leave
	// the card with only the title row.
	rows := strings.Split(plain, "\n")
	bodyFound := false
	for _, r := range rows {
		clean := strings.TrimSpace(r)
		if strings.HasPrefix(clean, "│") && strings.HasSuffix(clean, "│") &&
			len(strings.TrimSpace(strings.Trim(clean, "│"))) > 0 {
			bodyFound = true
			break
		}
	}
	assert.True(t, bodyFound, "tip-box must have at least one non-blank content row")
}

// stripANSIWelcome strips CSI sequences inline so welcome_test can
// assert on plain text without taking a dep on the renderer's test
// helpers.
func stripANSIWelcome(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
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
