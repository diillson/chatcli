/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// The bus is process-global: these tests do not run in parallel.

func TestRelevantContextRetrievalIsReported(t *testing.T) {
	rec := pulsetest.Record(t)
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	t.Cleanup(m.WaitGraphPersist)
	if !m.RememberFact("SECRET deploy pipeline uses helm charts", "project") {
		t.Fatal("seed fact")
	}

	out := m.GetRelevantContext([]string{"deploy", "helm"})
	ev := rec.WaitFor(t, "plain retrieval end", pulsetest.Is(pulse.KindBackground, pulseRetrieveNode, pulse.PhaseEnd))
	if ev.Status != pulse.StatusOK || ev.Parent != "" {
		t.Fatalf("plain retrieval = %+v", ev)
	}
	want := map[string]string{"hints": "2", "hits": "1", "hyde": "false", "vectors": "false"}
	for k, v := range want {
		if ev.Attrs[k] != v {
			t.Errorf("%s = %q, want %q (%+v)", k, ev.Attrs[k], v, ev.Attrs)
		}
	}
	if ev.Attrs["chars"] == "" || ev.Attrs["chars"] == "0" || len(out) == 0 {
		t.Errorf("chars = %q for %d bytes of context", ev.Attrs["chars"], len(out))
	}

	point := rec.WaitFor(t, "remember point", pulsetest.Is(pulse.KindBackground, pulseRememberNode, pulse.PhasePoint))
	if point.Attrs["category"] != "project" || point.Attrs["added"] != "true" {
		t.Errorf("remember point = %+v", point)
	}
	m.RememberFact("SECRET deploy pipeline uses helm charts", "project") // duplicate
	rec.WaitFor(t, "duplicate remember point", func(ev pulse.Event) bool {
		return pulsetest.Is(pulse.KindBackground, pulseRememberNode, pulse.PhasePoint)(ev) && ev.Attrs["added"] == "false"
	})

	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), "helm") {
		t.Fatalf("fact content leaked: %s", wire)
	}
}

// With HyDE the retrieval hangs from the turn in ctx, the HyDE pattern hangs
// from the retrieval, and the hypothesis call runs under the pattern.
func TestHyDERetrievalNestsUnderItsParent(t *testing.T) {
	rec := pulsetest.Record(t)
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	t.Cleanup(m.WaitGraphPersist)
	m.RememberFact("prefix invalidation breaks the keepalive window", "architecture")

	var llmParent string
	aug := NewHyDEAugmenter(HyDEConfig{Enabled: true, NumKeywords: 3}, func(ctx context.Context, _ string) (string, error) {
		llmParent = pulse.ParentFrom(ctx)
		return "prefix invalidation and keepalive windows", nil
	}, nil)

	ctx := pulse.WithParent(context.Background(), "turn-5")
	m.GetRelevantContextWithHyDE(ctx, "why does the cache miss", []string{"cache"}, aug)

	ret := rec.WaitFor(t, "hyde retrieval end", pulsetest.Is(pulse.KindBackground, pulseRetrieveNode, pulse.PhaseEnd))
	if ret.Parent != "turn-5" || ret.Attrs["hyde"] != "true" || ret.Attrs["hits"] == "0" {
		t.Fatalf("retrieval = %+v", ret)
	}
	hyde := rec.WaitFor(t, "hyde pattern end", pulsetest.Is(pulse.KindPattern, pulse.PatternRAGHyDE, pulse.PhaseEnd))
	if hyde.Parent != ret.ID {
		t.Errorf("HyDE parent = %q, want the retrieval %q", hyde.Parent, ret.ID)
	}
	if llmParent != hyde.ID {
		t.Errorf("hypothesis call ran under %q, want the HyDE span %q", llmParent, hyde.ID)
	}
}
