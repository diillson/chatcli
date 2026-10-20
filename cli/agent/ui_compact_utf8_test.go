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

// straddle puts a two-byte "é" across byte offset n.
func straddle(n, extra int) string {
	return strings.Repeat("a", n-1) + "é" + strings.Repeat("b", extra)
}

func clean(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, utf8.RuneError)
}

func TestCompactRenderers_CutKeepsUTF8Valid(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleCompact)
	cases := map[string]func(){
		"tool result minimal": func() { r.RenderToolResultMinimal(straddle(240, 50), false) },
		"compact line":        func() { r.CompactLine("•", "label", straddle(117, 50), ColorGreen) },
		"compact error":       func() { r.CompactError(straddle(117, 50)) },
	}
	for name, fn := range cases {
		if out := captureStdout(t, fn); !clean(out) {
			t.Errorf("%s: output shows a split character", name)
		}
	}
	if label := CompactToolLabel("read", straddle(57, 30)); !clean(label) {
		t.Errorf("CompactToolLabel = %q", label)
	}
}

func TestProcessManagerTruncateOutput_KeepsUTF8Valid(t *testing.T) {
	pm := NewProcessManager(nil)
	// maxBytes 1000: head and tail are 400 bytes each.
	out := straddle(400, 3000) + "é" + strings.Repeat("z", 399)
	got, truncated := pm.TruncateOutput(out, 1000)
	if !truncated || !utf8.ValidString(got) {
		t.Fatalf("truncated=%v valid=%v", truncated, utf8.ValidString(got))
	}
}
