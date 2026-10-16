/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package runs

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
)

func TestBeginRecordsThePulseParentAndBecomesIt(t *testing.T) {
	g := NewRegistry(8)
	ctx := pulse.WithParent(context.Background(), "turn-3")
	child, run := g.Begin(ctx, Info{Kind: KindMoA, Agent: "moa"})
	defer run.End(nil)
	snap := run.Snapshot()
	if snap.PulseParent != "turn-3" {
		t.Fatalf("PulseParent = %q, want the span it started under", snap.PulseParent)
	}
	if snap.ParentID != "" {
		t.Fatalf("a span must not enter the run tree, ParentID = %q", snap.ParentID)
	}
	if got := pulse.ParentFrom(child); got != snap.ID {
		t.Fatalf("work under the run must hang from it: ParentFrom = %q, want %q", got, snap.ID)
	}
	_, member := g.Begin(child, Info{Kind: KindMoA, Agent: "openai:gpt"})
	defer member.End(nil)
	if m := member.Snapshot(); m.ParentID != snap.ID || m.PulseParent != snap.ID {
		t.Fatalf("nested run = %+v", m)
	}
}
