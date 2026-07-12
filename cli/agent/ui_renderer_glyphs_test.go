package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRenderToolResultGlyphs covers both branches of the full and minimal
// tool-result cards: the title icon must come from the canonical kit
// vocabulary (single-width check/cross), never the emoji trio.
func TestRenderToolResultGlyphs(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)

	okOut := captureStdout(t, func() { r.RenderToolResult("done", false) })
	errOut := captureStdout(t, func() { r.RenderToolResult("boom", true) })
	assert.Contains(t, stripANSI(okOut), "✓", "success card must use the kit check")
	assert.Contains(t, stripANSI(errOut), "✗", "error card must use the kit cross")
	assert.NotContains(t, okOut, "✅")
	assert.NotContains(t, errOut, "❌")

	okMin := captureStdout(t, func() { r.RenderToolResultMinimal("done", false) })
	errMin := captureStdout(t, func() { r.RenderToolResultMinimal("boom", true) })
	assert.Contains(t, stripANSI(okMin), "✓")
	assert.Contains(t, stripANSI(errMin), "✗")
}

// TestPrintPlanCompactStatuses drives the three status branches (pending,
// success, failure) of the compact plan view.
func TestPrintPlanCompactStatuses(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)
	blocks := []CommandBlock{
		{Description: "first", Commands: []string{"echo 1"}},
		{Description: "second", Commands: []string{"echo 2"}},
		{Description: "third", Commands: []string{"echo 3"}},
	}
	outputs := []*CommandOutput{
		{Output: "ok"},                 // success
		{Output: "", ErrorMsg: "boom"}, // failure
		// third has no output yet → pending/running
	}

	out := stripANSI(captureStdout(t, func() { r.PrintPlanCompact(blocks, outputs) }))
	assert.Contains(t, out, "✓", "completed block uses the check")
	assert.Contains(t, out, "✗", "failed block uses the cross")
	assert.Contains(t, out, "↻", "pending block uses the running glyph")
	assert.Contains(t, out, "first")
	assert.Contains(t, out, "third")
}

// TestPrintHeaderAndBatchHeaderUseTitledRules asserts the fixed-width dash
// sandwiches are gone: both headers render as a single titled rule whose
// dashes span responsively.
func TestPrintHeaderAndBatchHeaderUseTitledRules(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)

	head := stripANSI(captureStdout(t, func() { r.PrintHeader() }))
	batch := stripANSI(captureStdout(t, func() { r.RenderBatchHeader(3) }))

	for name, out := range map[string]string{"PrintHeader": head, "RenderBatchHeader": batch} {
		assert.Contains(t, out, "──", "%s must draw a rule", name)
		assert.NotContains(t, out, "━", "%s must not use the legacy heavy dashes", name)
		assert.NotContains(t, out, "═", "%s must not use the legacy double lines", name)
	}
	// Titled rule shape: dashes on both sides of the title line.
	firstRule := ""
	for _, ln := range strings.Split(batch, "\n") {
		if strings.Contains(ln, "──") {
			firstRule = ln
			break
		}
	}
	if firstRule == "" || !strings.HasPrefix(strings.TrimSpace(firstRule), "──") {
		t.Errorf("batch header rule not titled: %q", firstRule)
	}
}
