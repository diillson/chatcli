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

// FuzzParseSearchLine hammers the permissive grep-line parser with arbitrary
// input. Invariants: never panics; when it claims a parse, the path is
// non-blank and the line number non-negative (digit-only Atoi).
func FuzzParseSearchLine(f *testing.F) {
	f.Add("src/main.go:42:func main() {")
	f.Add(`C:\src\main.go:42:hit`)
	f.Add("pre-commit-config.yaml-7-  hooks:")
	f.Add("no separators here")
	f.Add("::0::")
	f.Add("a:1:b:2:c:3")
	f.Add("--")
	f.Add(strings.Repeat(":", 100))
	f.Fuzz(func(t *testing.T, line string) {
		path, lineNo, _, _, ok := parseSearchLine(line)
		if !ok {
			return
		}
		if strings.TrimSpace(path) == "" {
			t.Fatalf("parse claimed ok with blank path for %q", line)
		}
		if lineNo < 0 {
			t.Fatalf("negative line number %d for %q", lineNo, line)
		}
	})
}

// FuzzHeuristicSkeleton hammers the language-agnostic code skeletonizer.
// Invariants: never panics; when it claims a skeleton, it kept at least one
// line and elided at least one (its own contract for claiming a reduction).
func FuzzHeuristicSkeleton(f *testing.F) {
	f.Add("def foo():\n    return 1\n\nclass Bar:\n    def baz(self):\n        pass\n" + strings.Repeat("x = 1\n", 10))
	f.Add(strings.Repeat("line without structure\n", 20))
	f.Add("func main() {\n\tfmt.Println(1)\n}\n" + strings.Repeat("\tstmt()\n", 12))
	f.Add("")
	f.Fuzz(func(t *testing.T, src string) {
		skeleton, lang, ok := heuristicSkeleton(src)
		if !ok {
			return
		}
		if lang != "generic" {
			t.Fatalf("unexpected lang %q", lang)
		}
		if skeleton == "" {
			t.Fatal("ok skeleton is empty")
		}
	})
}

// FuzzLayerMarkersAlwaysRecallable is the package's core contract as a fuzz
// target: whatever the payload and whichever compressor engages, every CCR
// marker present in the output MUST be recallable, and the output must remain
// valid UTF-8 when the input was. Runs against a deliberately tiny store so
// the per-entry capacity path is exercised too.
func FuzzLayerMarkersAlwaysRecallable(f *testing.F) {
	var grep strings.Builder
	for i := 0; i < 50; i++ {
		grep.WriteString("dir/file.go:10: some match content here\n")
	}
	f.Add(grep.String())
	f.Add("[" + strings.Repeat(`{"id":1,"name":"x"},`, 40) + `{"id":2}]`)
	f.Add(strings.Repeat("2026-07-02 INFO noise\n", 30) + "ERROR boom\npanic: dead\n")
	f.Add(strings.Repeat("# H\n→ text é ú\n", 200))
	f.Fuzz(func(t *testing.T, content string) {
		layer := NewLayer(Config{
			Mode:      ModeLossyWithCCR,
			Threshold: 64,
			Store:     NewBoundedMemoryStore(2048), // per-entry cap: 512 bytes
		})
		out, res := layer.CompressToolOutput("", content)

		for _, key := range ExtractKeys(out) {
			if _, ok := layer.Recall(key); !ok {
				t.Fatalf("unrecallable marker %s (strategy=%s)", key, res.Strategy)
			}
		}
		if utf8.ValidString(content) && !utf8.ValidString(out) {
			t.Fatalf("valid UTF-8 input produced invalid UTF-8 output (strategy=%s)", res.Strategy)
		}
		if res.CompressedSize > res.OriginalSize {
			t.Fatalf("compression grew the payload %d -> %d (strategy=%s)",
				res.OriginalSize, res.CompressedSize, res.Strategy)
		}
	})
}
