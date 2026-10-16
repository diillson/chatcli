/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package scheduler

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// parentAct records the dashboard parent its execution context carries.
type parentAct struct {
	parent atomic.Value
}

func (p *parentAct) Type() ActionType                    { return ActionType("parent_act") }
func (p *parentAct) ValidateSpec(_ map[string]any) error { return nil }
func (p *parentAct) Execute(ctx context.Context, _ Action, _ *ExecEnv) ActionResult {
	p.parent.Store(pulse.ParentFrom(ctx))
	return ActionResult{Output: "ran"}
}

// The job node says who owns it, which action it runs, its schedule kind and
// when it fires next; the action runs under the job's node, so what it
// starts hangs from the job.
func TestSchedulerJobNodeCarriesKindAndParentsItsAction(t *testing.T) {
	rec := pulsetest.Record(t)
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false
	s, err := New(cfg, NewNoopBridge(), SchedulerDeps{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pa := &parentAct{}
	_ = s.Actions().Register(pa)
	cfg.ActionAllowlist[ActionType("parent_act")] = true
	s.cfg = cfg
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.DrainAndShutdown(2 * time.Second)

	created, err := s.Enqueue(ctx, NewJob("probe", Owner{Kind: OwnerAgent, ID: "a"},
		Schedule{Kind: ScheduleRelative, Relative: 2 * time.Second},
		Action{Type: ActionType("parent_act"), Payload: map[string]any{"prompt": "SECRET"}}))
	if err != nil {
		t.Fatal(err)
	}
	node := pulseJobNodeID(created.ID)
	made := rec.WaitFor(t, "job created", func(ev pulse.Event) bool {
		return ev.ID == node && ev.Attrs["state"] == string(EventJobCreated)
	})
	if made.Attrs["owner"] != "agent" || made.Attrs["action"] != "parent_act" || made.Attrs["schedule"] != "relative" {
		t.Fatalf("created attrs: %+v", made.Attrs)
	}
	if n, err := strconv.Atoi(made.Attrs["next_fire_in_s"]); err != nil || n < 1 || n > 2 {
		t.Fatalf("next_fire_in_s: %+v", made.Attrs)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.Match(func(ev pulse.Event) bool { return ev.ID == node && ev.Phase == pulse.PhaseEnd })) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("job never ended: %+v", rec.Events())
		}
		time.Sleep(20 * time.Millisecond)
	}
	run := rec.WaitFor(t, "job running", func(ev pulse.Event) bool { return ev.ID == node && ev.Phase == pulse.PhaseStart })
	if run.Attrs["action"] != "parent_act" {
		t.Fatalf("running attrs: %+v", run.Attrs)
	}
	if got, _ := pa.parent.Load().(string); got != node {
		t.Fatalf("action parent = %q, want %q", got, node)
	}
	for _, ev := range rec.Events() {
		wire, _ := json.Marshal(ev)
		if strings.Contains(string(wire), "SECRET") {
			t.Fatalf("job payload leaked: %s", wire)
		}
	}
}

func TestSchedulerNextFireAcceptsRelayedTime(t *testing.T) {
	ts := time.Now()
	got, _ := pulseEvent(Event{Type: EventJobCompleted, JobID: "j", Timestamp: ts,
		Data: map[string]any{nextFireDataKey: ts.Add(90 * time.Second).Format(time.RFC3339Nano)}})
	if got.Attrs["next_fire_in_s"] != "90" {
		t.Fatalf("relayed next fire: %+v", got.Attrs)
	}
	got, _ = pulseEvent(Event{Type: EventJobCompleted, JobID: "j", Timestamp: ts, Data: map[string]any{nextFireDataKey: time.Time{}}})
	if _, ok := got.Attrs["next_fire_in_s"]; ok {
		t.Fatalf("a job with no next fire must not report one: %+v", got.Attrs)
	}
}
