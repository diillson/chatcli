/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"errors"
	"strings"
	"testing"
)

// fakeCompressor is a test double whose behavior is fully scripted.
type fakeCompressor struct {
	name       string
	confidence float64
	result     Result
	err        error
}

func (f *fakeCompressor) Name() string                { return f.name }
func (f *fakeCompressor) Detect(string, Hint) float64 { return f.confidence }
func (f *fakeCompressor) Compress(content string, _ Options) (Result, error) {
	if f.err != nil {
		return Result{}, f.err
	}
	r := f.result
	r.OriginalSize = len(content)
	return r, nil
}

func shrink(strategy string, out string, reversible bool) Result {
	return Result{Compressed: out, CompressedSize: len(out), Strategy: strategy, Reversible: reversible}
}

func TestRouterModeOffIsPassthrough(t *testing.T) {
	r := NewContentRouter(&fakeCompressor{name: "x", confidence: 1, result: shrink("x", "tiny", true)})
	in := "some large content"
	got := r.Compress(in, Hint{}, Options{Mode: ModeOff})
	if got.Compressed != in || got.Strategy != "passthrough" {
		t.Fatalf("ModeOff should passthrough; got strategy=%q compressed=%q", got.Strategy, got.Compressed)
	}
}

func TestRouterBelowThresholdIsPassthrough(t *testing.T) {
	r := NewContentRouter(&fakeCompressor{name: "x", confidence: 1, result: shrink("x", "y", true)})
	in := "short"
	got := r.Compress(in, Hint{}, Options{Mode: ModeLossyWithCCR, Threshold: 1000})
	if got.Strategy != "passthrough" {
		t.Fatalf("below threshold should passthrough, got %q", got.Strategy)
	}
}

func TestRouterPicksHighestConfidence(t *testing.T) {
	low := &fakeCompressor{name: "low", confidence: 0.6, result: shrink("low", "LOW", true)}
	high := &fakeCompressor{name: "high", confidence: 0.9, result: shrink("high", "HI", true)}
	r := NewContentRouter(low, high)
	got := r.Compress(strings.Repeat("x", 100), Hint{}, Options{Mode: ModeLossyWithCCR})
	if got.Strategy != "high" {
		t.Fatalf("router picked %q, want highest-confidence 'high'", got.Strategy)
	}
}

func TestRouterBelowConfidenceFloorIsPassthrough(t *testing.T) {
	weak := &fakeCompressor{name: "weak", confidence: 0.2, result: shrink("weak", "W", true)}
	r := NewContentRouter(weak)
	got := r.Compress(strings.Repeat("x", 100), Hint{}, Options{Mode: ModeLossyWithCCR})
	if got.Strategy != "passthrough" {
		t.Fatalf("sub-floor confidence should passthrough, got %q", got.Strategy)
	}
}

func TestRouterRejectsIrreversibleResult(t *testing.T) {
	// A compressor that claims a reduction but marks it irreversible must be
	// discarded — this is the core "never degrade" guarantee.
	bad := &fakeCompressor{name: "bad", confidence: 1, result: shrink("bad", "X", false)}
	r := NewContentRouter(bad)
	in := strings.Repeat("y", 100)
	got := r.Compress(in, Hint{}, Options{Mode: ModeLossyWithCCR})
	if got.Strategy != "passthrough" || got.Compressed != in {
		t.Fatalf("irreversible result must fall back to passthrough; got strategy=%q", got.Strategy)
	}
}

func TestRouterRejectsNonShrinkingResult(t *testing.T) {
	// "Compressed" output that is not smaller than the input is discarded.
	bloat := &fakeCompressor{name: "bloat", confidence: 1, result: shrink("bloat", strings.Repeat("z", 500), true)}
	r := NewContentRouter(bloat)
	in := strings.Repeat("y", 100)
	got := r.Compress(in, Hint{}, Options{Mode: ModeLossyWithCCR})
	if got.Strategy != "passthrough" {
		t.Fatalf("non-shrinking result must passthrough, got %q", got.Strategy)
	}
}

func TestRouterCompressorErrorDegradesSafely(t *testing.T) {
	boom := &fakeCompressor{name: "boom", confidence: 1, err: errors.New("kaboom")}
	r := NewContentRouter(boom)
	in := strings.Repeat("y", 100)
	got := r.Compress(in, Hint{}, Options{Mode: ModeLossyWithCCR})
	if got.Strategy != "passthrough" || got.Compressed != in {
		t.Fatalf("compressor error must degrade to passthrough, got strategy=%q", got.Strategy)
	}
}

func TestRouterRecordsMetrics(t *testing.T) {
	m := NewMetrics()
	good := &fakeCompressor{name: "good", confidence: 1, result: shrink("good", "tiny", true)}
	r := NewContentRouter(good)
	r.Compress(strings.Repeat("x", 100), Hint{}, Options{Mode: ModeLossyWithCCR, Metrics: m})
	snap := m.Snapshot()
	if snap.Calls != 1 || snap.Reductions != 1 {
		t.Fatalf("metrics not recorded: %+v", snap)
	}
	if snap.SavedBytes() != int64(100-len("tiny")) {
		t.Fatalf("SavedBytes = %d, want %d", snap.SavedBytes(), 100-len("tiny"))
	}
}

func TestModeParsing(t *testing.T) {
	cases := map[string]Mode{
		"off": ModeOff, "lossless": ModeLosslessOnly, "lossy-with-ccr": ModeLossyWithCCR, "": ModeLossyWithCCR,
	}
	for in, want := range cases {
		if got, ok := ParseMode(in); got != want || !ok {
			t.Fatalf("ParseMode(%q) = %v ok=%v, want %v", in, got, ok, want)
		}
	}
	if got, ok := ParseMode("garbage"); ok || got != ModeLossyWithCCR {
		t.Fatalf("ParseMode(garbage) = %v ok=%v, want lossy-with-ccr,false", got, ok)
	}
}
