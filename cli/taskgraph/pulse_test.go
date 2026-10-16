/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package taskgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// Every attempt at a task is a pattern node under the graph's run, closed
// with how the attempt ended; a task skipped because its dependency failed
// is a blocked point under the same run. No task text travels.
func TestEnginePulseAttemptsAndBlocked(t *testing.T) {
	rec := pulsetest.Record(t)
	g, store := testGraph(t, `{"name":"x","tasks":[
		{"id":"T1","prompt":"secret prompt text","max_attempts":2},
		{"id":"T2","prompt":"after","deps":["T1"]}
	]}`)
	disp := newFakeDispatcher()
	disp.failExecutorOnce["tg:T1:e1"] = true
	disp.reviewFailOnce["tg:T1:r2"] = true
	e := newTestEngine(t, g, store, disp, &fakeGate{})
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	blocked := rec.WaitFor(t, "blocked point", pulsetest.Is(pulse.KindPattern, pulseTaskNode, pulse.PhasePoint))
	if blocked.Status != pulse.StatusBlocked || blocked.Attrs["task"] != "" || blocked.Attrs["index"] != "1" || blocked.Attrs["deps"] != "1" {
		t.Fatalf("blocked point: %+v", blocked)
	}
	ends := rec.Match(pulsetest.Is(pulse.KindPattern, pulseTaskNode, pulse.PhaseEnd))
	if len(ends) != 2 {
		t.Fatalf("want 2 attempt nodes, got %d: %+v", len(ends), ends)
	}
	want := []struct{ attempt, state string }{{"1", pulseOutcomeExecError}, {"2", pulseOutcomeReviewFail}}
	for i, ev := range ends {
		if ev.Status != pulse.StatusError || ev.Attrs["attempt"] != want[i].attempt || ev.Attrs["state"] != want[i].state ||
			ev.Attrs["task"] != "" || ev.Attrs["index"] != "0" || ev.Attrs["max_attempts"] != "2" {
			t.Fatalf("attempt %d: %+v", i+1, ev)
		}
		if !strings.HasPrefix(ev.Parent, "run-") || ev.Parent != blocked.Parent {
			t.Fatalf("attempt and blocked point must hang from the graph run: %q vs %q", ev.Parent, blocked.Parent)
		}
		for _, v := range ev.Attrs {
			if v == "secret prompt text" {
				t.Fatalf("task text leaked: %+v", ev)
			}
		}
	}
}

func TestEnginePulseAttemptPass(t *testing.T) {
	rec := pulsetest.Record(t)
	g, store := testGraph(t, `{"name":"x","tasks":[{"id":"T1","prompt":"work"}]}`)
	e := newTestEngine(t, g, store, newFakeDispatcher(), &fakeGate{})
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev := rec.WaitFor(t, "attempt end", pulsetest.Is(pulse.KindPattern, pulseTaskNode, pulse.PhaseEnd))
	if ev.Status != pulse.StatusOK || ev.Attrs["state"] != pulseOutcomePass || ev.Attrs["attempt"] != "1" || ev.DurMS == 0 {
		t.Fatalf("attempt: %+v", ev)
	}
}
