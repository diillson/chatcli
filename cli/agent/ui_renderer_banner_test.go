/*
 * ChatCLI - UIRenderer banner + menu tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"github.com/stretchr/testify/assert"
)

// init seeds the global i18n printer so tests in this file can rely on
// the user-facing strings (which carry the emojis we assert on). Without
// it, i18n.T returns the raw key and substring matches for "✅" / "⚠"
// would fail even when the production code is correct.
func init() { i18n.Init() }

// TestRenderModeBanner_FieldsAlignAndIconShows locks the layout of the
// unified mode banner shared by /coder and /agent. Critical because the
// emoji-vs-runewidth alignment fix is what makes this card balanced —
// any future change to RenderTimelineEvent that re-breaks that
// invariant would show up here as misaligned borders or a missing icon.
func TestRenderModeBanner_FieldsAlignAndIconShows(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)
	out := captureStdout(t, func() {
		r.RenderModeBanner("🛠 ", "CODER MODE", ColorCyan, [][2]string{
			{"Objetivo", "implementar X"},
			{"Workspace", "/tmp/proj"},
		})
	})

	plain := stripANSI(out)
	assert.Contains(t, plain, "🛠")
	assert.Contains(t, plain, "CODER MODE")
	assert.Contains(t, plain, "Objetivo")
	assert.Contains(t, plain, "implementar X")
	assert.Contains(t, plain, "Workspace")
	assert.Contains(t, plain, "/tmp/proj")

	// Border invariant: top and bottom must have matching width.
	var top, bot string
	for _, ln := range strings.Split(plain, "\n") {
		if strings.HasPrefix(ln, "╭") {
			top = ln
		}
		if strings.HasPrefix(ln, "╰") {
			bot = ln
		}
	}
	assert.NotEmpty(t, top, "top border present")
	assert.NotEmpty(t, bot, "bottom border present")
	assert.Equal(t, VisibleLen(top), VisibleLen(bot),
		"top and bottom borders must have equal visible width")
}

// TestRenderModeBanner_EmptyFields covers the degenerate case: callers
// can hand the banner zero fields (e.g. an entry banner with just a
// title). The card should still render with the title on top and a
// minimal body — no panic, no missing border.
func TestRenderModeBanner_EmptyFields(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)
	out := captureStdout(t, func() {
		r.RenderModeBanner("🤖", "AGENT MODE", ColorLime, nil)
	})
	plain := stripANSI(out)
	assert.Contains(t, plain, "🤖 AGENT MODE")
	assert.Contains(t, plain, "╭")
	assert.Contains(t, plain, "╰")
}

// TestPrintMenu_ColumnsAndKeys proves the three-column reorganization
// of the agent menu still emits every menu key the user expects. The
// columns are built via lipgloss.JoinHorizontal so a layout regression
// (column rendered empty, separator missing) would show as a missing
// key in this assertion list.
func TestPrintMenu_ColumnsAndKeys(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)
	out := captureStdout(t, func() { r.PrintMenu() })
	plain := stripANSI(out)

	// Every navigation key the menu used to list — none can vanish.
	for _, key := range []string{
		"[1..N]", "a", "cN", "eN", "tN", "pcN", "acN", "vN", "wN", "p", "r", "q",
	} {
		assert.Contains(t, plain, key, "menu must keep key %q after the columnar redesign", key)
	}
}

// TestRenderBatchSummary_OkVsError covers both branches of the batch
// footer line — the success path (green ✅) and the error path (yellow
// ⚠ — kept yellow on purpose because partial success is a warning, not
// a failure; we don't want it to read as the same severity as a hard
// error card).
func TestRenderBatchSummary_OkVsError(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)
	okOut := captureStdout(t, func() { r.RenderBatchSummary(3, 3, false) })
	errOut := captureStdout(t, func() { r.RenderBatchSummary(1, 3, true) })

	assert.Contains(t, okOut, "✅", "success must use the green check glyph")
	assert.Contains(t, okOut, ColorGreen)
	assert.Contains(t, errOut, "⚠")
	assert.Contains(t, errOut, ColorYellow, "partial-success uses yellow (warning), not red")
}

// TestCompactBatchSummary_OnlyOnMultipleActions documents the silent
// branch in CompactBatchSummary: it deliberately prints NOTHING for
// single-action runs because the per-call ✓/✗ line already conveys
// the outcome. If a future change makes it always print, sessions
// with one tool call will end with redundant "1/1 completed".
func TestCompactBatchSummary_OnlyOnMultipleActions(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleCompact)
	silent := captureStdout(t, func() { r.CompactBatchSummary(1, 1, false) })
	loud := captureStdout(t, func() { r.CompactBatchSummary(3, 3, false) })

	assert.Empty(t, silent,
		"single-action success must be silent — the per-call ✓ already speaks")
	assert.Contains(t, loud, "✓")
}
