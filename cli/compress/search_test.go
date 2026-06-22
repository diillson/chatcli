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

func TestParseSearchLineUnix(t *testing.T) {
	p, n, c, isMatch, ok := parseSearchLine("cli/agent_mode.go:945:func process()")
	if !ok || p != "cli/agent_mode.go" || n != 945 || c != "func process()" || !isMatch {
		t.Fatalf("got path=%q line=%d content=%q match=%v ok=%v", p, n, c, isMatch, ok)
	}
}

func TestParseSearchLineWindowsDriveLetter(t *testing.T) {
	// The drive colon must not be mistaken for the line-number separator.
	p, n, _, _, ok := parseSearchLine(`C:\Users\dev\main.go:42:hit`)
	if !ok || p != `C:\Users\dev\main.go` || n != 42 {
		t.Fatalf("windows path mis-parsed: path=%q line=%d ok=%v", p, n, ok)
	}
}

func TestParseSearchLineDashInFilename(t *testing.T) {
	// ripgrep context line, path contains dashes. Anchor on the earliest
	// "<sep>\d+<sep>" so the path survives.
	p, n, c, isMatch, ok := parseSearchLine("pre-commit-config.yaml-42-some content")
	if !ok || p != "pre-commit-config.yaml" || n != 42 || c != "some content" || isMatch {
		t.Fatalf("dash filename mis-parsed: path=%q line=%d content=%q match=%v ok=%v", p, n, c, isMatch, ok)
	}
}

func TestParseSearchLineRejectsNonMatches(t *testing.T) {
	for _, ln := range []string{"", "--", "plain text", "no:colon:digits", "path:notnumber:x"} {
		if _, _, _, _, ok := parseSearchLine(ln); ok {
			t.Fatalf("expected %q to NOT parse as a search line", ln)
		}
	}
}

func TestSearchDetect(t *testing.T) {
	c := NewSearchCompressor()
	if got := c.Detect("anything", Hint{ToolName: "@search"}); got < 0.9 {
		t.Fatalf("tool hint should be high confidence, got %v", got)
	}
	dense := "a.go:1:x\na.go:2:y\nb.go:3:z\nc.go:4:w"
	if got := c.Detect(dense, Hint{}); got < 0.75 {
		t.Fatalf("dense search output should detect, got %v", got)
	}
	if got := c.Detect("just\nsome\nprose\nhere", Hint{}); got != 0 {
		t.Fatalf("prose should not detect as search, got %v", got)
	}
}

func TestSearchCompressDropsAndOffloadsToCCR(t *testing.T) {
	// 4 files x 10 matches = 40 matches; caps should drop a lot.
	var sb strings.Builder
	for f := 0; f < 4; f++ {
		for ln := 1; ln <= 10; ln++ {
			fmt.Fprintf(&sb, "pkg/file%d.go:%d:match content number %d\n", f, ln, ln)
		}
	}
	original := sb.String()
	store := NewMemoryStore()
	c := NewSearchCompressor()
	res, err := c.Compress(original, Options{Mode: ModeLossyWithCCR, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompressedSize >= res.OriginalSize {
		t.Fatalf("expected reduction, got %d >= %d", res.CompressedSize, res.OriginalSize)
	}
	if !res.Reversible || res.CacheKey == "" {
		t.Fatalf("lossy drop must be reversible with a CCR key: %+v", res)
	}
	// The original must be byte-recoverable from the store.
	got, ok, _ := store.Get(res.CacheKey)
	if !ok || got != original {
		t.Fatal("CCR did not preserve the byte-identical original")
	}
	// The marker must appear in the compressed output.
	if !strings.Contains(res.Compressed, FormatMarker(res.CacheKey)) {
		t.Fatal("compressed output is missing the @recall marker")
	}
	// At most MaxMatchesPerFile per file in the output.
	if strings.Count(res.Compressed, "  L") > c.MaxTotalMatches {
		t.Fatalf("kept more than the global cap: %d", strings.Count(res.Compressed, "  L"))
	}
}

func TestSearchLosslessWhenNoStore(t *testing.T) {
	// Without a CCR store the compressor must NOT drop any match. The only
	// reduction allowed is the lossless group-by-file reformat (de-duplicating
	// the repeated path prefix). Every line number must survive and no CCR
	// marker may be emitted.
	var sb strings.Builder
	for ln := 1; ln <= 50; ln++ {
		fmt.Fprintf(&sb, "a.go:%d:line %d\n", ln, ln)
	}
	c := NewSearchCompressor()
	res, _ := c.Compress(sb.String(), Options{Mode: ModeLossyWithCCR, Store: nil})
	if res.CacheKey != "" {
		t.Fatal("must not offload without a store")
	}
	for ln := 1; ln <= 50; ln++ {
		if !strings.Contains(res.Compressed, fmt.Sprintf("L%d: line %d", ln, ln)) {
			t.Fatalf("lossless reformat dropped line %d", ln)
		}
	}
}

func TestSearchKeepsErrorLines(t *testing.T) {
	var sb strings.Builder
	for ln := 1; ln <= 20; ln++ {
		fmt.Fprintf(&sb, "a.go:%d:ordinary line %d\n", ln, ln)
	}
	sb.WriteString("a.go:21:panic: runtime error here\n")
	c := NewSearchCompressor()
	res, _ := c.Compress(sb.String(), Options{Mode: ModeLossyWithCCR, Store: NewMemoryStore()})
	if !strings.Contains(res.Compressed, "panic: runtime error") {
		t.Fatal("error line should be prioritized and kept")
	}
}
