/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A long tool result is cut for display; the cut must not split a
// multi-byte character. The wrap re-encodes the text by rune, so a split
// byte reached the screen as U+FFFD at the end of the truncated result.
func TestRenderToolResult_TruncationKeepsUTF8Valid(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)
	// 1999 ASCII bytes, then "é" (2 bytes) straddling the 2000-byte cap.
	output := strings.Repeat("a", 1999) + "é" + strings.Repeat("b", 500)
	for _, style := range []UIStyle{UIStyleFull, UIStyleCompact, UIStyleMinimal} {
		r.style = style
		out := captureStdout(t, func() { r.RenderToolResult(output, false) })
		if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
			t.Errorf("style %v: the truncated tool result shows a split character", style)
		}
	}
}
