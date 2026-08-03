/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package runs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestBeginRegistersAndEndRetires(t *testing.T) {
	reg := NewRegistry(10)
	ctx, run := reg.Begin(context.Background(), Info{
		Kind:  KindWorker,
		Agent: "coder",
		Task:  "implement feature",
	})
	if run == nil {
		t.Fatal("Begin returned nil run")
	}
	if got := FromContext(ctx); got != run {
		t.Fatal("context does not carry the run handle")
	}

	active := reg.Active()
	if len(active) != 1 {
		t.Fatalf("expected 1 active run, got %d", len(active))
	}
	if active[0].Status != StatusRunning {
		t.Fatalf("expected running, got %s", active[0].Status)
	}

	run.End(nil)
	if len(reg.Active()) != 0 {
		t.Fatal("run still active after End")
	}
	recent := reg.Recent(0)
	if len(recent) != 1 || recent[0].Status != StatusCompleted {
		t.Fatalf("expected 1 completed run in history, got %+v", recent)
	}
	if recent[0].Elapsed() < 0 {
		t.Fatal("negative elapsed on finished run")
	}
}

func TestEndStatusMapping(t *testing.T) {
	cases := []struct {
		err  error
		want Status
	}{
		{nil, StatusCompleted},
		{context.Canceled, StatusCancelled},
		{fmt.Errorf("wrapped: %w", context.Canceled), StatusCancelled},
		{errors.New("boom"), StatusFailed},
	}
	for _, tc := range cases {
		reg := NewRegistry(4)
		_, run := reg.Begin(context.Background(), Info{Kind: KindWorker})
		run.End(tc.err)
		got := reg.Recent(1)
		if len(got) != 1 || got[0].Status != tc.want {
			t.Fatalf("err=%v: expected %s, got %+v", tc.err, tc.want, got)
		}
	}
}

func TestEndIsIdempotent(t *testing.T) {
	reg := NewRegistry(4)
	_, run := reg.Begin(context.Background(), Info{Kind: KindWorker})
	run.End(nil)
	run.End(errors.New("late error must not override"))
	recent := reg.Recent(0)
	if len(recent) != 1 {
		t.Fatalf("double End duplicated history: %d entries", len(recent))
	}
	if recent[0].Status != StatusCompleted {
		t.Fatalf("second End overwrote status: %s", recent[0].Status)
	}
}

func TestParentInheritedFromContext(t *testing.T) {
	reg := NewRegistry(10)
	ctx, parent := reg.Begin(context.Background(), Info{Kind: KindOrchestrator})
	_, child := reg.Begin(ctx, Info{Kind: KindWorker, Agent: "reviewer"})

	if got := child.Snapshot().ParentID; got != parent.ID() {
		t.Fatalf("child not parented: got %q want %q", got, parent.ID())
	}
	kids := reg.Children(parent.ID())
	if len(kids) != 1 || kids[0].ID != child.ID() {
		t.Fatalf("Children lookup failed: %+v", kids)
	}
}

func TestExplicitParentWins(t *testing.T) {
	reg := NewRegistry(10)
	ctx, _ := reg.Begin(context.Background(), Info{Kind: KindOrchestrator})
	_, child := reg.Begin(ctx, Info{Kind: KindSubagent, ParentID: "run-999"})
	if got := child.Snapshot().ParentID; got != "run-999" {
		t.Fatalf("explicit ParentID overridden: %q", got)
	}
}

func TestCancelPropagatesThroughContext(t *testing.T) {
	reg := NewRegistry(10)
	ctx, run := reg.Begin(context.Background(), Info{Kind: KindWorker, Agent: "tester"})

	if !reg.Cancel(run.ID()) {
		t.Fatal("Cancel returned false for a live run")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context not cancelled within 1s")
	}
	run.End(ctx.Err())
	got, ok := reg.Get(run.ID())
	if !ok || got.Status != StatusCancelled {
		t.Fatalf("expected cancelled, got %+v ok=%v", got, ok)
	}
	if reg.Cancel(run.ID()) {
		t.Fatal("Cancel must return false for finished runs")
	}
}

func TestProgressUpdatesVisibleInSnapshots(t *testing.T) {
	reg := NewRegistry(10)
	_, run := reg.Begin(context.Background(), Info{Kind: KindWorker, CallID: "agent_1"})
	run.SetTurn(3, 30)
	run.SetAction("read cli/foo.go")
	run.AddToolCalls(2)
	run.AddToolCalls(1)

	snap, ok := reg.ByCallID("agent_1")
	if !ok {
		t.Fatal("ByCallID missed a live run")
	}
	if snap.Turn != 3 || snap.MaxTurns != 30 || snap.Action != "read cli/foo.go" || snap.ToolCalls != 3 {
		t.Fatalf("progress not reflected: %+v", snap)
	}
}

func TestHistoryRingCapacity(t *testing.T) {
	reg := NewRegistry(3)
	for i := 0; i < 5; i++ {
		_, run := reg.Begin(context.Background(), Info{Kind: KindWorker, Task: fmt.Sprintf("t%d", i)})
		run.End(nil)
	}
	recent := reg.Recent(0)
	if len(recent) != 3 {
		t.Fatalf("ring exceeded capacity: %d", len(recent))
	}
	// Newest first: t4, t3, t2.
	if recent[0].Task != "t4" || recent[2].Task != "t2" {
		t.Fatalf("ring order wrong: %+v", recent)
	}
}

func TestGetFindsFinishedRuns(t *testing.T) {
	reg := NewRegistry(4)
	_, run := reg.Begin(context.Background(), Info{Kind: KindMoA, Agent: "member-1"})
	id := run.ID()
	run.End(nil)
	got, ok := reg.Get(id)
	if !ok || got.ID != id || got.Status != StatusCompleted {
		t.Fatalf("Get after End failed: %+v ok=%v", got, ok)
	}
}

func TestNilSafety(t *testing.T) {
	var run *Run
	run.SetTurn(1, 2)
	run.SetAction("x")
	run.AddToolCalls(1)
	run.End(nil)
	if run.ID() != "" {
		t.Fatal("nil run ID must be empty")
	}
	if s := run.Snapshot(); s.ID != "" {
		t.Fatal("nil run snapshot must be zero")
	}

	var reg *Registry
	if reg.Active() != nil || reg.Recent(1) != nil || reg.Children("x") != nil {
		t.Fatal("nil registry listings must be nil")
	}
	if _, ok := reg.Get("x"); ok {
		t.Fatal("nil registry Get must miss")
	}
	if reg.Cancel("x") {
		t.Fatal("nil registry Cancel must be false")
	}
	ctx, r := reg.Begin(context.Background(), Info{})
	if r != nil || ctx == nil {
		t.Fatal("nil registry Begin must return nil run and original ctx")
	}
	var nilCtx context.Context
	if FromContext(nilCtx) != nil {
		t.Fatal("FromContext on a nil context must be nil")
	}
}

func TestConcurrentUse(t *testing.T) {
	reg := NewRegistry(50)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ctx, run := reg.Begin(context.Background(), Info{
				Kind: KindWorker, CallID: fmt.Sprintf("c%d", n),
			})
			for t := 1; t <= 5; t++ {
				run.SetTurn(t, 5)
				run.SetAction("step")
				run.AddToolCalls(1)
				_ = reg.Active()
				_, _ = reg.ByCallID(fmt.Sprintf("c%d", n))
			}
			_, sub := reg.Begin(ctx, Info{Kind: KindSubagent})
			sub.End(nil)
			run.End(nil)
		}(i)
	}
	wg.Wait()
	if n := len(reg.Active()); n != 0 {
		t.Fatalf("%d runs leaked in active set", n)
	}
	if n := len(reg.Recent(0)); n != 50 {
		t.Fatalf("expected full ring (50), got %d", n)
	}
}

func TestAllMergesActiveAndRecent(t *testing.T) {
	reg := NewRegistry(10)
	_, done := reg.Begin(context.Background(), Info{Kind: KindWorker, Task: "done"})
	done.End(nil)
	_, live := reg.Begin(context.Background(), Info{Kind: KindWorker, Task: "live"})
	defer live.End(nil)

	all := reg.All(5)
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
	if all[0].Task != "live" || all[1].Task != "done" {
		t.Fatalf("All order wrong: %+v", all)
	}
	SortByStart(all)
	if all[0].Task != "done" {
		t.Fatalf("SortByStart wrong: %+v", all)
	}
}
