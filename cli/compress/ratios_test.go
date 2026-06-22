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

// TestCompressionRatios is a measurement harness mirroring Headroom's headline
// table: it runs each strategy on a representative large payload and reports the
// reduction. It doubles as a no-degradation guard — every reduction must be
// reversible (CCR key set) and the stored original must round-trip byte-for-byte.
//
// Run it with:  go test ./cli/compress/ -run TestCompressionRatios -v
func TestCompressionRatios(t *testing.T) {
	type scenario struct {
		name  string
		hint  Hint
		input string
	}
	scenarios := []scenario{
		{"code search (grep)", Hint{ToolName: "@search"}, genSearch(120)},
		{"build/test log", Hint{ToolName: "build"}, genLog(800)},
		{"git diff", Hint{ToolName: "git diff"}, genDiff(40)},
		{"JSON array", Hint{}, genJSONArray(300)},
		{"code skeleton (Go)", Hint{MIME: "code"}, genGoFile(60)},
		{"web prose (boilerplate)", Hint{ToolName: "@webfetch"}, genWebProse(40)},
	}

	router := newDefaultRouter()
	t.Logf("%-26s %10s %10s %8s  %-12s", "scenario", "in(bytes)", "out", "reduced", "strategy")
	t.Logf("%s", strings.Repeat("-", 76))
	for _, s := range scenarios {
		store := NewMemoryStore()
		res := router.Compress(s.input, s.hint, Options{Mode: ModeLossyWithCCR, Store: store, Metrics: NewMetrics()})
		reduced := 100 * (1 - res.Ratio())
		t.Logf("%-26s %10d %10d %7.0f%%  %-12s", s.name, res.OriginalSize, res.CompressedSize, reduced, res.Strategy)

		if res.Strategy == "passthrough" {
			t.Errorf("%s: expected a real reduction, got passthrough", s.name)
			continue
		}
		if res.CompressedSize >= res.OriginalSize {
			t.Errorf("%s: did not reduce (%d >= %d)", s.name, res.CompressedSize, res.OriginalSize)
		}
		if !res.Reversible {
			t.Errorf("%s: reduction is not reversible", s.name)
		}
		// No-degradation guard: whatever was offloaded must round-trip exactly.
		if res.CacheKey != "" {
			if got, ok, _ := store.Get(res.CacheKey); !ok || got != s.input {
				t.Errorf("%s: CCR original did not round-trip byte-for-byte", s.name)
			}
		}
	}
}

func genSearch(n int) string {
	var b strings.Builder
	for f := 0; f < n/8; f++ {
		for ln := 1; ln <= 8; ln++ {
			fmt.Fprintf(&b, "internal/pkg/module%d/handler.go:%d:\tresult := process(req, opts) // call %d\n", f, ln*7, ln)
		}
	}
	return b.String()
}

func genLog(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		switch {
		case i%200 == 7:
			b.WriteString("ERROR failed to connect to upstream service: connection refused\n")
		case i%97 == 0:
			b.WriteString("WARN retry budget low\n")
		default:
			fmt.Fprintf(&b, "INFO 2026-06-22T12:00:%02d processed request id=%d latency=12ms\n", i%60, i)
		}
	}
	b.WriteString("=== 1 failed, 1421 passed in 18.3s ===\n")
	return b.String()
}

func genDiff(n int) string {
	var b strings.Builder
	b.WriteString("diff --git a/server.go b/server.go\nindex aaa..bbb 100644\n--- a/server.go\n+++ b/server.go\n@@ -1,200 +1,200 @@\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, " unchanged context line %d that stays the same\n", i)
	}
	b.WriteString("-\told := legacyHandler()\n+\tnew := modernHandler()\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, " more unchanged trailing context line %d\n", i)
	}
	return b.String()
}

func genJSONArray(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf(`{"id":%d,"name":"item-%d","active":true,"score":%d}`, i, i, i*3))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func genGoFile(n int) string {
	var b strings.Builder
	b.WriteString("package service\n\nimport (\n\t\"fmt\"\n\t\"context\"\n)\n\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "// Func%d does work number %d.\nfunc Func%d(ctx context.Context, x int) (int, error) {\n\ty := x * %d\n\tfor i := 0; i < y; i++ {\n\t\ty += i\n\t}\n\tfmt.Println(y)\n\treturn y, nil\n}\n\n", i, i, i, i+1)
	}
	return b.String()
}

func genWebProse(n int) string {
	nav := "Home\nProducts\nDocs\nBlog\nContact\nSign in\nSkip to main content\nAccept cookies\n"
	var b strings.Builder
	b.WriteString("# Documentation\n\n")
	for p := 0; p < n; p++ {
		b.WriteString(nav)
		fmt.Fprintf(&b, "## Article %d\n\nThis is a unique paragraph of real documentation content for section %d.\n\n\n\n", p, p)
	}
	return b.String()
}
