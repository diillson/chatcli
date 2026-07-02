/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestProseSectionTrimPreservesUTF8 guards against byte-slicing a section at a
// position that lands mid-rune. Web content is routinely non-ASCII (pt-BR
// accents, arrows, CJK); a head/tail cut that splits a UTF-8 sequence injects
// invalid bytes into the model's prompt. Three-byte runes make every cut
// position that is not a multiple of 3 land mid-rune.
func TestProseSectionTrimPreservesUTF8(t *testing.T) {
	c := NewProseCompressor()
	opts := Options{Mode: ModeLossyWithCCR, Store: NewMemoryStore()}

	// One heading, then a single section line of contiguous 3-byte runes
	// ("→" is U+2192). The head cut lands at SectionCap*2/3 and the tail cut
	// at len-SectionCap/3; with the section being one unbroken run of 3-byte
	// sequences, any cut position not divisible by 3 is guaranteed mid-rune —
	// which is the case for the default caps (4000 and 2000 are not multiples
	// of 3, and the run length is).
	content := "# Título\n" + strings.Repeat("→", c.SectionCap)

	res, err := c.Compress(content, opts)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if res.Strategy == "passthrough" || res.CompressedSize >= res.OriginalSize {
		t.Fatalf("fixture did not engage the section trimmer (strategy=%s, %d->%d bytes)",
			res.Strategy, res.OriginalSize, res.CompressedSize)
	}
	if !utf8.ValidString(res.Compressed) {
		t.Fatal("section trim produced invalid UTF-8 (cut mid-rune)")
	}
}

// TestProseSectionTrimCounterNeverNegative pins the accounting on the
// degenerate shape: a section that is one enormous line (minified HTML turned
// Markdown). Trimming it head/tail adds gap lines, so a naive
// before-minus-after line count goes negative and corrupts Detail.
func TestProseSectionTrimCounterNeverNegative(t *testing.T) {
	c := NewProseCompressor()
	opts := Options{Mode: ModeLossyWithCCR, Store: NewMemoryStore()}

	content := "# H\n" + strings.Repeat("x", c.SectionCap*3) // single giant line

	res, err := c.Compress(content, opts)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if res.Strategy == "passthrough" {
		t.Fatal("fixture did not engage the section trimmer")
	}
	if got := res.Detail["lines_removed"]; got < 0 {
		t.Fatalf("Detail[lines_removed] = %d; counters must never go negative", got)
	}
}
