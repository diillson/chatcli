/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package runs

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestActivityLog_RecordsLifecycleAndSurvivesRetire(t *testing.T) {
	reg := NewRegistry(2)
	_, run := reg.Begin(context.Background(), Info{Kind: KindWorker, Agent: "coder", Task: "t"})
	id := run.ID()

	log, ok := reg.ActivityLog(id)
	if !ok || len(log) != 0 {
		t.Fatalf("fresh run: ok=%v len=%d", ok, len(log))
	}

	run.SetTurn(1, 10)
	run.SetAction("read main.go")
	run.SetAction("") // empty labels are not logged
	run.Note("output: " + strings.Repeat("x", 5000))
	run.End(errors.New("boom"))

	log, ok = reg.ActivityLog(id)
	if !ok {
		t.Fatal("finished run must keep its log")
	}
	want := []string{"turn 1/10", "action: read main.go", "output: ", "end status=failed"}
	if len(log) != len(want) {
		t.Fatalf("log = %d entries, want %d: %+v", len(log), len(want), log)
	}
	for i, prefix := range want {
		if !strings.HasPrefix(log[i].Text, prefix) || log[i].At.IsZero() {
			t.Fatalf("entry %d = %+v, want prefix %q", i, log[i], prefix)
		}
	}
	if len(log[2].Text) > maxActivityTextBytes+len("…") || !strings.HasSuffix(log[2].Text, "…") {
		t.Fatalf("note not bounded: len=%d", len(log[2].Text))
	}
	if !strings.Contains(log[3].Text, "error=boom") {
		t.Fatalf("end entry missing error: %q", log[3].Text)
	}

	// Returned slices are copies.
	log[0].Text = "mutated"
	again, _ := reg.ActivityLog(id)
	if again[0].Text == "mutated" {
		t.Fatal("ActivityLog must return a copy")
	}

	// Eviction from the finished ring drops the log too.
	for i := 0; i < 2; i++ {
		_, r := reg.Begin(context.Background(), Info{Kind: KindWorker, Agent: "x"})
		r.End(nil)
	}
	if _, ok := reg.ActivityLog(id); ok {
		t.Fatal("evicted run must not keep its log")
	}
	if _, ok := reg.ActivityLog("run-nope"); ok {
		t.Fatal("unknown id must report false")
	}
}

func TestActivityLog_Bounded(t *testing.T) {
	reg := NewRegistry(1)
	_, run := reg.Begin(context.Background(), Info{Kind: KindWorker, Agent: "coder"})
	for i := 0; i < DefaultActivityCap+50; i++ {
		run.SetAction("step")
	}
	log, _ := reg.ActivityLog(run.ID())
	if len(log) != DefaultActivityCap {
		t.Fatalf("log = %d entries, want cap %d", len(log), DefaultActivityCap)
	}
	var nilRun *Run
	nilRun.Note("ignored") // nil-safe
}
