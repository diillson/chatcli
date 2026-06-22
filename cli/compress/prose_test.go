/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"fmt"
	"strings"
	"testing"
)

func TestProseDetectScope(t *testing.T) {
	c := NewProseCompressor()
	// Web/reference tool output and explicit prose hint engage it.
	if got := c.Detect("anything", Hint{ToolName: "@webfetch"}); got < 0.6 {
		t.Fatalf("webfetch output should engage prose, got %v", got)
	}
	if got := c.Detect("anything", Hint{MIME: "markdown"}); got < 0.9 {
		t.Fatalf("explicit markdown hint should engage, got %v", got)
	}
	// Local file reads and unknown sources must NOT engage (never rewrite
	// content an agent may be editing).
	if got := c.Detect("# Heading\n\ntext", Hint{ToolName: "@read"}); got != 0 {
		t.Fatalf("prose must not auto-fire on @read, got %v", got)
	}
	if got := c.Detect("# Heading\n\ntext", Hint{}); got != 0 {
		t.Fatalf("prose must not sniff arbitrary content, got %v", got)
	}
}

func TestProseDedupBoilerplate(t *testing.T) {
	// Simulate a web scrape: the same nav/footer lines repeat on every "page",
	// interspersed with unique content.
	nav := "Home\nProducts\nAbout\nContact\nSkip to content\n"
	var sb strings.Builder
	for page := 0; page < 8; page++ {
		sb.WriteString(nav)
		fmt.Fprintf(&sb, "Unique article paragraph number %d with real content.\n", page)
		sb.WriteString("\n\n\n") // blank runs
	}
	original := sb.String()
	store := NewMemoryStore()
	c := NewProseCompressor()
	res, err := c.Compress(original, Options{Mode: ModeLossyWithCCR, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompressedSize >= res.OriginalSize {
		t.Fatalf("boilerplate dedup should reduce, got %d >= %d", res.CompressedSize, res.OriginalSize)
	}
	if !res.Reversible || res.CacheKey == "" {
		t.Fatalf("prose reduction must be reversible: %+v", res)
	}
	// Every unique paragraph must survive — we only drop duplicates.
	for page := 0; page < 8; page++ {
		want := fmt.Sprintf("Unique article paragraph number %d", page)
		if !strings.Contains(res.Compressed, want) {
			t.Fatalf("unique content dropped: %q", want)
		}
	}
	// The nav lines should appear once, not eight times.
	if n := strings.Count(res.Compressed, "Skip to content"); n != 1 {
		t.Fatalf("boilerplate line kept %d times, want 1", n)
	}
	// Original recoverable byte-for-byte.
	if got, ok, _ := store.Get(res.CacheKey); !ok || got != original {
		t.Fatal("CCR did not preserve the byte-identical prose")
	}
}

func TestProseKeepsHeadingsTrimsLongSection(t *testing.T) {
	c := NewProseCompressor()
	c.SectionCap = 500
	var sb strings.Builder
	sb.WriteString("# Title\n\n")
	sb.WriteString("## Section A\n\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&sb, "Sentence %d of a very long section that should be trimmed.\n", i)
	}
	sb.WriteString("## Section B\n\nShort.\n")
	original := sb.String()
	res, _ := c.Compress(original, Options{Mode: ModeLossyWithCCR, Store: NewMemoryStore()})
	if res.CacheKey == "" {
		t.Fatalf("long section should trigger trimming + offload: %+v", res)
	}
	for _, h := range []string{"# Title", "## Section A", "## Section B"} {
		if !strings.Contains(res.Compressed, h) {
			t.Fatalf("heading %q must survive trimming", h)
		}
	}
	if !strings.Contains(res.Compressed, "chars of this section omitted") {
		t.Fatal("long section should be head/tail trimmed")
	}
}

func TestProseLosslessWithoutStore(t *testing.T) {
	c := NewProseCompressor()
	original := strings.Repeat("Home\nAbout\n\n", 50)
	res, _ := c.Compress(original, Options{Mode: ModeLossyWithCCR, Store: nil})
	if res.Strategy != "passthrough" {
		t.Fatalf("no store => passthrough, got %q", res.Strategy)
	}
}

func TestProseNoBoilerplatePassthrough(t *testing.T) {
	// Content with no duplicates/blank runs and short sections: nothing to do.
	c := NewProseCompressor()
	original := "# Doc\n\nA single unique paragraph with no repetition at all here.\n"
	res, _ := c.Compress(original, Options{Mode: ModeLossyWithCCR, Store: NewMemoryStore()})
	if res.CacheKey != "" {
		t.Fatalf("nothing to reduce must not offload, got key %q", res.CacheKey)
	}
}
