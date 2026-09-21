/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

func TestHyDEReportsWhetherItAugmentedRetrieval(t *testing.T) {
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(32)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	answer := func(out string, err error) AugmenterFunc {
		return func(context.Context, string) (string, error) { return out, err }
	}
	cfg := HyDEConfig{Enabled: true, NumKeywords: 3}
	hints := []string{"cache"}

	got := NewHyDEAugmenter(cfg, answer("SECRET-HYPOTHESIS about prefix invalidation and keepalive windows", nil), nil).Augment(context.Background(), "SECRET-QUERY", hints)
	if len(got) <= len(hints) {
		t.Fatalf("hints were not augmented: %v", got)
	}
	NewHyDEAugmenter(cfg, answer("", errors.New("SECRET-ERROR")), nil).Augment(context.Background(), "q", hints)
	NewHyDEAugmenter(cfg, answer("   ", nil), nil).Augment(context.Background(), "q", hints)
	NewHyDEAugmenter(HyDEConfig{}, answer("x", nil), nil).Augment(context.Background(), "q", hints) // disabled: silent
	NewHyDEAugmenter(cfg, answer("x", nil), nil).Augment(context.Background(), "  ", hints)         // empty query: silent

	var ends []pulse.Event
	deadline := time.After(3 * time.Second)
	for len(ends) < 3 {
		select {
		case ev := <-ch:
			if ev.Kind == pulse.KindPattern && ev.Phase == pulse.PhaseEnd {
				ends = append(ends, ev)
			}
		case <-deadline:
			t.Fatalf("got %d of 3 end events", len(ends))
		}
	}
	want := []struct{ status, state string }{
		{pulse.StatusOK, "augmented retrieval"}, {pulse.StatusError, "fell back to plain hints"}, {pulse.StatusOK, "empty hypothesis"},
	}
	for i, w := range want {
		if ends[i].Name != pulse.PatternRAGHyDE || ends[i].Status != w.status || ends[i].Attrs["state"] != w.state {
			t.Errorf("case %d = %+v, want %s/%s", i, ends[i], w.status, w.state)
		}
	}
	if ends[0].Attrs["keywords"] != "3" {
		t.Errorf("keywords = %q, want the capped count", ends[0].Attrs["keywords"])
	}
	select {
	case ev := <-ch:
		if ev.Kind == pulse.KindPattern {
			t.Fatalf("a disabled or empty-query HyDE must stay silent: %+v", ev)
		}
	case <-time.After(60 * time.Millisecond):
	}
	wire, _ := json.Marshal(ends)
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("query, hypothesis or error text leaked: %s", wire)
	}
}
