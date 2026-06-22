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

func TestLayerDisabledIsPassthrough(t *testing.T) {
	l := NewLayer(Config{Mode: ModeOff})
	in := strings.Repeat("a.go:1:x\n", 100)
	out, res := l.CompressToolOutput("@search", in)
	if out != in || res.Strategy != "passthrough" {
		t.Fatalf("disabled layer must passthrough; got strategy=%q", res.Strategy)
	}
}

func TestLayerBelowThresholdUntouched(t *testing.T) {
	l := NewLayer(Config{Mode: ModeLossyWithCCR, Store: NewMemoryStore(), Threshold: 10_000})
	in := "a.go:1:tiny\n"
	out, _ := l.CompressToolOutput("@search", in)
	if out != in {
		t.Fatal("below-threshold output must be byte-identical")
	}
}

func TestLayerCompressAndRecallRoundTrip(t *testing.T) {
	l := NewLayer(Config{Mode: ModeLossyWithCCR, Store: NewMemoryStore(), Threshold: 100})
	var sb strings.Builder
	for f := 0; f < 5; f++ {
		for ln := 1; ln <= 12; ln++ {
			fmt.Fprintf(&sb, "pkg/file%d.go:%d:some matching content here %d\n", f, ln, ln)
		}
	}
	original := sb.String()
	out, res := l.CompressToolOutput("@search", original)
	if res.CacheKey == "" {
		t.Fatal("expected a CCR offload for a large search payload")
	}
	if len(out) >= len(original) {
		t.Fatal("expected reduction")
	}
	// The @recall path returns the byte-identical original.
	recalled, ok := l.Recall(res.CacheKey)
	if !ok || recalled != original {
		t.Fatal("Recall did not return the byte-identical original")
	}
	// Metrics reflect the activity.
	stats, store := l.Stats()
	if stats.Calls != 1 || stats.CCRPuts != 1 || stats.CCRHits != 1 {
		t.Fatalf("unexpected metrics: %+v", stats)
	}
	if store.Entries != 1 {
		t.Fatalf("expected 1 CCR entry, got %d", store.Entries)
	}
}

func TestLayerRoutesByContentType(t *testing.T) {
	l := NewLayer(Config{Mode: ModeLossyWithCCR, Store: NewMemoryStore(), Threshold: 100})

	// A log payload should be handled by the log strategy.
	var logs strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&logs, "INFO heartbeat %d\n", i)
	}
	logs.WriteString("ERROR something failed badly\n")
	_, res := l.CompressToolOutput("", logs.String())
	if res.Strategy != "log" {
		t.Fatalf("log payload routed to %q, want log", res.Strategy)
	}
}

func TestNewLayerFromEnvUsesDiskStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CHATCLI_COMPRESSION", "lossy-with-ccr")
	t.Setenv("CHATCLI_COMPRESSION_CCR_DIR", dir)
	l := NewLayerFromEnv("")
	if !l.Enabled() || l.Mode() != ModeLossyWithCCR {
		t.Fatalf("env layer not enabled in lossy mode: mode=%v", l.Mode())
	}
	in := strings.Repeat("x.go:1:match\n", 500)
	_, res := l.CompressToolOutput("@search", in)
	if res.CacheKey != "" {
		// Offload happened: the on-disk store should now hold a file.
		if got, ok := l.Recall(res.CacheKey); !ok || got != in {
			t.Fatal("disk-backed recall failed")
		}
	}
}

func TestLayerSetModeRuntimeToggle(t *testing.T) {
	// Start off; the store is still built so a runtime switch to lossy works.
	dir := t.TempDir()
	t.Setenv("CHATCLI_COMPRESSION", "off")
	t.Setenv("CHATCLI_COMPRESSION_CCR_DIR", dir)
	l := NewLayerFromEnv("")
	big := strings.Repeat("a.go:1:match\n", 500)

	if _, res := l.CompressToolOutput("@search", big); res.Strategy != "passthrough" {
		t.Fatalf("off mode must passthrough, got %q", res.Strategy)
	}
	// Flip to lossy at runtime — compression must now engage and offload.
	l.SetMode(ModeLossyWithCCR)
	out, res := l.CompressToolOutput("@search", big)
	if res.CacheKey == "" || len(out) >= len(big) {
		t.Fatalf("runtime switch to lossy did not engage compression: %+v", res)
	}
	if got, ok := l.Recall(res.CacheKey); !ok || got != big {
		t.Fatal("offloaded original not recoverable after runtime toggle")
	}
}

func TestLayerIdempotentOnAlreadyCompressed(t *testing.T) {
	// Output that already carries a CCR marker (e.g. a sub-agent compressed it
	// before returning to the parent) must not be re-compressed/re-offloaded.
	l := NewLayer(Config{Mode: ModeLossyWithCCR, Store: NewMemoryStore(), Threshold: 50})
	already := "some result " + FormatMarker(KeyFor("x")) + " " + strings.Repeat("y", 500)
	out, res := l.CompressHinted(Hint{ToolName: "@search"}, already)
	if out != already || res.Strategy != "passthrough" {
		t.Fatalf("already-compressed content must pass through unchanged, got %q strategy=%s", res.Strategy, res.Strategy)
	}
	if stats, _ := l.Stats(); stats.CCRPuts != 0 {
		t.Fatalf("must not offload a second copy, CCRPuts=%d", stats.CCRPuts)
	}
}

func TestNewLayerFromEnvOff(t *testing.T) {
	t.Setenv("CHATCLI_COMPRESSION", "off")
	l := NewLayerFromEnv(t.TempDir())
	if l.Enabled() {
		t.Fatal("CHATCLI_COMPRESSION=off must disable the layer")
	}
}
