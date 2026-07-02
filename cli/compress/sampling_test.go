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

// TestDetectionSamplesTailFailure pins the head+tail sampling property at
// scale: a multi-megabyte build log whose only signal (the failure) sits in
// the final lines must still route to the log compressor. A head-only sample
// would see megabytes of INFO noise and pass the payload through uncompressed.
func TestDetectionSamplesTailFailure(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50_000; i++ { // ~3.5MB of pure INFO noise
		fmt.Fprintf(&b, "2026-07-02T12:00:00 INFO compiling unit %d of 50000 ok\n", i)
	}
	b.WriteString("ERROR build failed: undefined symbol frobnicate\n")
	b.WriteString("--- FAIL: TestEverything (0.03s)\n")
	b.WriteString("FAIL\texample.com/pkg\t0.451s\n")

	l := NewLayer(Config{Mode: ModeLossyWithCCR, Store: NewMemoryStore(), Threshold: 100})
	out, res := l.CompressToolOutput("", b.String())

	if res.Strategy != "log" {
		t.Fatalf("tail-failure log routed to %q, want log", res.Strategy)
	}
	if !strings.Contains(out, "ERROR build failed") {
		t.Fatal("compressed log lost the failure line")
	}
	if res.CompressedSize >= res.OriginalSize {
		t.Fatal("log compressor did not shrink the payload")
	}
}

// TestDetectSampleLinesBounds pins the sampler's contract: bounded output,
// exact passthrough for small payloads, and head+tail composition for large
// ones.
func TestDetectSampleLinesBounds(t *testing.T) {
	small := "a\nb\nc"
	if got := detectSampleLines(small); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("small payload sample = %v", got)
	}

	var b strings.Builder
	for i := 0; i < 100_000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	sample := detectSampleLines(b.String())
	if len(sample) > detectSampleMaxLines {
		t.Fatalf("sample has %d lines, cap is %d", len(sample), detectSampleMaxLines)
	}
	if sample[0] != "line 0" {
		t.Fatalf("sample head = %q, want the payload's first line", sample[0])
	}
	if last := sample[len(sample)-1]; last != "line 99999" {
		t.Fatalf("sample tail = %q, want the payload's last line", last)
	}
}
