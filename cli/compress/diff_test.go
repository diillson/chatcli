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

func bigDiff() string {
	var sb strings.Builder
	sb.WriteString("diff --git a/main.go b/main.go\n")
	sb.WriteString("index 111..222 100644\n")
	sb.WriteString("--- a/main.go\n")
	sb.WriteString("+++ b/main.go\n")
	sb.WriteString("@@ -1,40 +1,40 @@\n")
	for i := 0; i < 18; i++ {
		fmt.Fprintf(&sb, " context line %d unchanged\n", i)
	}
	sb.WriteString("-old implementation line\n")
	sb.WriteString("+new implementation line\n")
	for i := 0; i < 18; i++ {
		fmt.Fprintf(&sb, " trailing context %d unchanged\n", i)
	}
	return sb.String()
}

func TestDiffDetect(t *testing.T) {
	c := NewDiffCompressor()
	if got := c.Detect(bigDiff(), Hint{}); got < 0.9 {
		t.Fatalf("git diff should detect strongly, got %v", got)
	}
	if got := c.Detect("not a diff\njust text\nat all here\n", Hint{}); got != 0 {
		t.Fatalf("non-diff should not detect, got %v", got)
	}
}

func TestDiffKeepsChangesTrimsContext(t *testing.T) {
	original := bigDiff()
	store := NewMemoryStore()
	c := NewDiffCompressor()
	res, err := c.Compress(original, Options{Mode: ModeLossyWithCCR, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if res.CompressedSize >= res.OriginalSize {
		t.Fatalf("expected context trimming to reduce size, got %d >= %d", res.CompressedSize, res.OriginalSize)
	}
	// Both change lines must survive.
	if !strings.Contains(res.Compressed, "-old implementation line") ||
		!strings.Contains(res.Compressed, "+new implementation line") {
		t.Fatal("change lines must always be kept")
	}
	// Headers survive.
	for _, h := range []string{"diff --git a/main.go b/main.go", "@@ -1,40 +1,40 @@", "+++ b/main.go"} {
		if !strings.Contains(res.Compressed, h) {
			t.Fatalf("header %q must be kept", h)
		}
	}
	if !res.Reversible || res.CacheKey == "" {
		t.Fatalf("lossy diff must be reversible: %+v", res)
	}
	if got, ok, _ := store.Get(res.CacheKey); !ok || got != original {
		t.Fatal("CCR did not preserve the byte-identical diff")
	}
	// Far-away context should be dropped.
	if strings.Contains(res.Compressed, "context line 0 unchanged") {
		t.Fatal("distant context should have been trimmed")
	}
}

func TestDiffLosslessWhenNoStore(t *testing.T) {
	c := NewDiffCompressor()
	res, _ := c.Compress(bigDiff(), Options{Mode: ModeLossyWithCCR, Store: nil})
	if res.Strategy != "passthrough" {
		t.Fatalf("no store => passthrough (context trim is lossy), got %q", res.Strategy)
	}
}
