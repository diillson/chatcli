/*
 * ChatCLI - tests for structure-preserving body wrap (chat envelope)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * These tests defend the fix for the user-reported bug where YAML/JSON/
 * code pasted into chat mode lost its indentation ("fica todo à esquerda
 * e perde a identação"). The root cause was wrapText collapsing leading
 * whitespace via strings.Fields; the envelope now routes its body through
 * wrapStructured, which preserves the relative indentation hierarchy.
 */

package agent

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leadingSpaces counts the visible leading-space indent of a line after
// stripping ANSI — mirrors what the user sees in the terminal.
func leadingSpaces(line string) int {
	plain := stripANSIForCard(line)
	return len(plain) - len(strings.TrimLeft(plain, " "))
}

// TestSplitLeadingIndent_ANSIAware proves the indent detector sees through
// the zero-width color escapes glamour emits *before* the indent spaces.
func TestSplitLeadingIndent_ANSIAware(t *testing.T) {
	// Glamour-shaped line: color codes, then 4 spaces, then a colored token.
	line := "\x1b[38;5;140m\x1b[0m\x1b[38;5;252m\x1b[0m    \x1b[38;5;140mkey\x1b[0m"
	indent, codes, content := splitLeadingIndent(line)
	assert.Equal(t, 4, indent, "must count the 4 leading spaces despite leading ANSI")
	assert.Contains(t, content, "key", "content keeps the visible token")
	assert.NotContains(t, stripANSIForCard(content), " ", "no leading spaces left in content")
	// The token's own color span must survive somewhere (codes or content).
	assert.Contains(t, codes+content, "\x1b[38;5;140m", "first token keeps its color")
}

// TestWrapStructured_PreservesRelativeIndent is the core regression: a
// YAML block rendered by glamour must keep its nesting after wrapping.
func TestWrapStructured_PreservesRelativeIndent(t *testing.T) {
	md := "Aqui vai um exemplo:\n\n```yaml\nmetadata:\n  annotations:\n    key: value\n```\n"
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle("dark"), glamour.WithWordWrap(0))
	require.NoError(t, err)
	rendered, err := r.Render(md)
	require.NoError(t, err)

	wrapped := wrapStructured(strings.Trim(rendered, "\n\r"), 80)

	indentOf := func(substr string) int {
		for _, ln := range wrapped {
			if strings.Contains(stripANSIForCard(ln), substr) {
				return leadingSpaces(ln)
			}
		}
		t.Fatalf("line containing %q not found in %#v", substr, wrapped)
		return -1
	}

	prose := indentOf("Aqui vai um exemplo")
	meta := indentOf("metadata:")
	anno := indentOf("annotations:")
	key := indentOf("key: value")

	// Prose dedented to the left edge (glamour's base margin removed; the
	// box's own Padding(0,2) supplies breathing room).
	assert.Equal(t, 0, prose, "prose sits flush-left inside the box")
	// The YAML nesting hierarchy is intact and strictly increasing.
	assert.Greater(t, meta, prose, "metadata indented past prose")
	assert.Greater(t, anno, meta, "annotations nested under metadata")
	assert.Greater(t, key, anno, "key nested under annotations")
}

// TestWrapStructured_VerbatimWhenFits asserts lines that fit the inner
// width are emitted untouched — internal column alignment preserved (the
// thing strings.Fields used to destroy).
func TestWrapStructured_VerbatimWhenFits(t *testing.T) {
	body := "  alpha:   1\n  beta:    22\n  gamma:   333"
	wrapped := wrapStructured(body, 80)
	require.Len(t, wrapped, 3)
	// Common indent (2) is removed, but the multi-space column alignment
	// inside each line stays exactly as-is.
	assert.Equal(t, "alpha:   1", wrapped[0])
	assert.Equal(t, "beta:    22", wrapped[1])
	assert.Equal(t, "gamma:   333", wrapped[2])
}

// TestWrapStructured_OverflowRepeatsIndent verifies that an overflowing
// indented line word-wraps with its indent repeated on continuations,
// and that no produced row exceeds the limit.
func TestWrapStructured_OverflowRepeatsIndent(t *testing.T) {
	limit := 20
	// A flush-left anchor line sets the common margin to 0, so the deeper
	// line keeps its relative indent of 4 instead of being dedented away.
	body := "raiz:\n    " + strings.Repeat("palavra ", 12)
	wrapped := wrapStructured(body, limit)
	cont := wrapped[1:] // everything after the "raiz:" anchor
	require.Greater(t, len(cont), 1, "long indented line must wrap into multiple rows")
	for _, ln := range cont {
		assert.LessOrEqual(t, VisibleLen(ln), limit, "no row exceeds the limit: %q", ln)
		assert.Equal(t, 4, leadingSpaces(ln), "indent repeated on every continuation: %q", ln)
	}
}

// TestWrapStructured_ProseStillWraps confirms plain prose (no structure)
// still word-wraps to the inner width — we didn't regress the common case.
func TestWrapStructured_ProseStillWraps(t *testing.T) {
	long := strings.Repeat("palavra ", 40)
	wrapped := wrapStructured(strings.TrimSpace(long), 30)
	require.Greater(t, len(wrapped), 1)
	for _, ln := range wrapped {
		assert.LessOrEqual(t, VisibleLen(ln), 30, "prose row within limit: %q", ln)
	}
}

// TestWrapStructured_BlankLinesPreserved keeps paragraph spacing intact.
func TestWrapStructured_BlankLinesPreserved(t *testing.T) {
	body := "linha um\n\nlinha dois"
	wrapped := wrapStructured(body, 80)
	require.Len(t, wrapped, 3)
	assert.Equal(t, "linha um", wrapped[0])
	assert.Equal(t, "", wrapped[1])
	assert.Equal(t, "linha dois", wrapped[2])
}
