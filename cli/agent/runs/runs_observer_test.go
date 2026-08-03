/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package runs

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestInstanceTokenNamespacesIDs pins the cross-process uniqueness contract:
// run IDs embed the registry's instance token, and snapshots carry it.
func TestInstanceTokenNamespacesIDs(t *testing.T) {
	reg := NewRegistry(10)
	if reg.Instance() == "" {
		t.Fatal("registry must mint an instance token")
	}
	_, run := reg.Begin(context.Background(), Info{Kind: KindWorker, Agent: "coder"})
	defer run.End(nil)

	snap := run.Snapshot()
	if !strings.HasPrefix(snap.ID, "run-"+reg.Instance()+"-") {
		t.Errorf("run ID %q must embed instance token %q", snap.ID, reg.Instance())
	}
	if snap.Instance != reg.Instance() {
		t.Errorf("snapshot Instance = %q, want %q", snap.Instance, reg.Instance())
	}
}

// TestOnEventObservesLifecycle pins the observer contract the hub bridge
// relies on: begin, progress mutations and end each deliver a snapshot.
func TestOnEventObservesLifecycle(t *testing.T) {
	reg := NewRegistry(10)

	var mu sync.Mutex
	var events []Info
	reg.OnEvent(func(info Info) {
		mu.Lock()
		events = append(events, info)
		mu.Unlock()
	})

	_, run := reg.Begin(context.Background(), Info{Kind: KindWorker, Agent: "tester"})
	run.SetTurn(1, 5)
	run.SetAction("read foo.go")
	run.AddToolCalls(2)
	run.End(nil)

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 5 {
		t.Fatalf("expected 5 events (begin, turn, action, tools, end), got %d", len(events))
	}
	if events[0].Status != StatusRunning {
		t.Errorf("first event must be the running begin snapshot, got %s", events[0].Status)
	}
	last := events[len(events)-1]
	if last.Status != StatusCompleted {
		t.Errorf("last event must be terminal, got %s", last.Status)
	}
	if last.ToolCalls != 2 || last.Turn != 1 || last.Action != "read foo.go" {
		t.Errorf("terminal snapshot must carry accumulated progress: %+v", last)
	}

	// Detach: no further events may arrive.
	reg.OnEvent(nil)
	n := len(events)
	_, run2 := reg.Begin(context.Background(), Info{Kind: KindWorker, Agent: "x"})
	run2.End(nil)
	if len(events) != n {
		t.Error("detached observer must not receive events")
	}
}
