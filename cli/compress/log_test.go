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

func TestLogDetectLevel(t *testing.T) {
	cases := map[string]logLevel{
		"2024-01-01 ERROR something broke": lvlError,
		"[WARN] disk almost full":          lvlWarn,
		"INFO starting up":                 lvlInfo,
		"DEBUG x=1":                        lvlDebug,
		"FAIL\tpkg/foo":                    lvlError,
		"just a plain line":                lvlUnknown,
		"TERRORISM dominated headlines":    lvlUnknown, // word-boundary guard (substring, not token)
	}
	for in, want := range cases {
		if got := detectLevel(in); got != want {
			t.Errorf("detectLevel(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestStackBlocksSurviveBlankLine(t *testing.T) {
	lines := []logLine{
		{n: 0, content: "Traceback (most recent call last)", isStack: true},
		{n: 1, content: `  File "a.py", line 10, in f`, isStack: true},
		{n: 2, content: "", isStack: false}, // blank mid-trace
		{n: 3, content: `  File "b.py", line 20, in g`, isStack: true},
	}
	blocks := stackBlocks(lines)
	if len(blocks) != 1 {
		t.Fatalf("blank line split the trace into %d blocks, want 1", len(blocks))
	}
	if len(blocks[0]) != 4 {
		t.Fatalf("trace block has %d lines, want 4 (blank absorbed)", len(blocks[0]))
	}
}

func TestWarnSignatureDedupe(t *testing.T) {
	a := warnSignature("deprecation warning: api X removed in 2.0")
	b := warnSignature("deprecation warning: api Y removed in 3.0")
	if a != b {
		t.Fatalf("same warning head should share signature: %q vs %q", a, b)
	}
	c := warnSignature("unused variable: foo")
	if a == c {
		t.Fatal("distinct warning categories must not collapse")
	}
}

func TestLogCompressKeepsErrorsDropsNoise(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "INFO routine heartbeat tick %d\n", i)
	}
	sb.WriteString("ERROR database connection refused at host db-1\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "DEBUG cache lookup %d\n", i)
	}
	original := sb.String()
	store := NewMemoryStore()
	c := NewLogCompressor()
	res, err := c.Compress(original, Options{Mode: ModeLossyWithCCR, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompressedSize >= res.OriginalSize {
		t.Fatalf("expected major reduction, got %d >= %d", res.CompressedSize, res.OriginalSize)
	}
	if !strings.Contains(res.Compressed, "ERROR database connection refused") {
		t.Fatal("the single error line must be kept")
	}
	if !res.Reversible || res.CacheKey == "" {
		t.Fatalf("lossy log compression must be reversible: %+v", res)
	}
	got, ok, _ := store.Get(res.CacheKey)
	if !ok || got != original {
		t.Fatal("CCR did not preserve the byte-identical original log")
	}
	if !strings.Contains(res.Compressed, "lines omitted") {
		t.Fatal("dropped runs should be summarized with a gap marker")
	}
}

func TestLogLosslessWhenNoStore(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&sb, "INFO tick %d\n", i)
	}
	sb.WriteString("ERROR boom\n")
	c := NewLogCompressor()
	res, _ := c.Compress(sb.String(), Options{Mode: ModeLossyWithCCR, Store: nil})
	if res.CacheKey != "" || res.Strategy != "passthrough" {
		t.Fatalf("no store => passthrough, got strategy=%q key=%q", res.Strategy, res.CacheKey)
	}
}

func TestLogDetect(t *testing.T) {
	c := NewLogCompressor()
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&sb, "INFO line %d\n", i)
	}
	sb.WriteString("ERROR failure\nWARN careful\n=== 1 failed, 39 passed ===\n")
	if got := c.Detect(sb.String(), Hint{}); got < 0.6 {
		t.Fatalf("log-shaped content should detect, got %v", got)
	}
	if got := c.Detect("one\ntwo\nthree", Hint{}); got != 0 {
		t.Fatalf("tiny prose should not detect as log, got %v", got)
	}
}
