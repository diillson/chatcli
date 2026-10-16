/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// The bus is process-global: these tests do not run in parallel.

func TestCompressionIsReportedWithSizesAndArchive(t *testing.T) {
	rec := pulsetest.Record(t)
	l := NewLayer(Config{Mode: ModeLossyWithCCR, Store: NewMemoryStore(), Threshold: 100})
	var sb strings.Builder
	for f := 0; f < 5; f++ {
		for ln := 1; ln <= 12; ln++ {
			fmt.Fprintf(&sb, "pkg/SECRET%d.go:%d:some matching content here %d\n", f, ln, ln)
		}
	}
	original := sb.String()
	_, res := l.CompressToolOutput("@search", original)
	if res.CacheKey == "" {
		t.Fatal("expected a CCR offload")
	}
	l.CompressToolOutput("@search", "tiny") // below threshold: no span

	ev := rec.WaitFor(t, "compress end", pulsetest.Is(pulse.KindBackground, pulseCompressNode, pulse.PhaseEnd))
	want := map[string]string{
		"tool":      "search",
		"strategy":  res.Strategy,
		"in_bytes":  strconv.Itoa(len(original)),
		"out_bytes": strconv.Itoa(res.CompressedSize),
		"archived":  "true",
	}
	for k, v := range want {
		if ev.Attrs[k] != v {
			t.Errorf("%s = %q, want %q", k, ev.Attrs[k], v)
		}
	}
	if r, err := strconv.ParseFloat(ev.Attrs["ratio"], 64); err != nil || r <= 0 || r >= 1 {
		t.Errorf("ratio = %q, want a reduction", ev.Attrs["ratio"])
	}

	if _, ok := l.Recall(res.CacheKey); !ok {
		t.Fatal("recall")
	}
	l.Recall("ccr_missing")
	isRecall := pulsetest.Is(pulse.KindBackground, pulseRecallNode, pulse.PhasePoint)
	rec.WaitFor(t, "two recall points", func(ev pulse.Event) bool { return isRecall(ev) && len(rec.Match(isRecall)) >= 2 })
	pts := rec.Match(isRecall)
	if pts[0].Attrs["hit"] != "true" || pts[0].Attrs["bytes"] != strconv.Itoa(len(original)) || pts[0].Status != pulse.StatusOK {
		t.Errorf("recall hit = %+v", pts[0])
	}
	if pts[1].Attrs["hit"] != "false" || pts[1].Status != pulse.StatusOK {
		t.Errorf("recall miss = %+v", pts[1])
	}

	if len(rec.Match(pulsetest.Is(pulse.KindBackground, pulseCompressNode, pulse.PhaseEnd))) != 1 {
		t.Error("a below-threshold payload must not open a span")
	}
	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), res.CacheKey) {
		t.Fatalf("content or keys leaked: %s", wire)
	}
}

func TestArchiveIsReportedStoredOrRefused(t *testing.T) {
	rec := pulsetest.Record(t)
	l := NewLayer(Config{Mode: ModeLossyWithCCR, Store: NewBoundedMemoryStore(400), Threshold: 100})

	if _, ok := l.Archive("short transcript"); !ok {
		t.Fatal("small archive must be stored")
	}
	if _, ok := l.Archive(strings.Repeat("x", 200)); ok {
		t.Fatal("an entry over the per-entry cap must be refused")
	}
	isArchive := pulsetest.Is(pulse.KindBackground, pulseArchiveNode, pulse.PhasePoint)
	rec.WaitFor(t, "two archive points", func(ev pulse.Event) bool { return isArchive(ev) && len(rec.Match(isArchive)) >= 2 })
	pts := rec.Match(isArchive)
	if pts[0].Status != pulse.StatusOK || pts[0].Attrs["archived"] != "true" || pts[0].Attrs["bytes"] != "16" {
		t.Errorf("stored archive = %+v", pts[0])
	}
	if pts[1].Status != pulse.StatusError || pts[1].Attrs["archived"] != "false" || pts[1].Attrs["bytes"] != "200" {
		t.Errorf("refused archive = %+v", pts[1])
	}
}
