/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package embedding

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

func TestInstrumentedEmbedShowsOnDashboard(t *testing.T) {
	rec := pulsetest.Record(t)
	p := Instrument(&fakeProvider{name: "bedrock:amazon.titan-embed-text-v2:0"})
	ctx := pulse.WithParent(context.Background(), "tool-42")
	if _, err := p.Embed(ctx, []string{"SECRET-TEXT", "other"}); err != nil {
		t.Fatal(err)
	}
	ev := rec.WaitFor(t, "embed end", pulsetest.Is(pulse.KindTool, "embed", pulse.PhaseEnd))
	want := map[string]string{
		"provider": "bedrock", "model": "amazon.titan-embed-text-v2:0", "engine": "remote",
		"texts": "2", "vectors": "2", "dims": "2",
	}
	for k, v := range want {
		if ev.Attrs[k] != v {
			t.Errorf("attr %s = %q, want %q (all: %v)", k, ev.Attrs[k], v, ev.Attrs)
		}
	}
	if ev.Parent != "tool-42" || ev.Status != pulse.StatusOK {
		t.Errorf("parent %q status %q", ev.Parent, ev.Status)
	}
	for _, e := range rec.Events() {
		for _, v := range e.Attrs {
			if v == "SECRET-TEXT" {
				t.Fatal("embedded text leaked onto the bus")
			}
		}
	}

	// A failed call ends as an error; a local engine says so.
	local := Instrument(&fakeProvider{name: "ollama:nomic-embed-text"})
	if _, err := local.Embed(context.Background(), nil); err == nil {
		t.Fatal("expected the fake's empty-batch error")
	}
	fail := rec.WaitFor(t, "failed embed", func(e pulse.Event) bool {
		return e.Kind == pulse.KindTool && e.Name == "embed" && e.Phase == pulse.PhaseEnd && e.Attrs["engine"] == "local"
	})
	if fail.Status != pulse.StatusError {
		t.Errorf("status %q, want error", fail.Status)
	}
}
