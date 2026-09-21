/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
)

// watchPatterns turns the process-wide bus on and collects pattern events.
func watchPatterns(t *testing.T) func(n int) []pulse.Event {
	t.Helper()
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(256)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })
	var seen []pulse.Event
	return func(n int) []pulse.Event {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for len(seen) < n {
			select {
			case ev := <-ch:
				if ev.Kind == pulse.KindPattern {
					seen = append(seen, ev)
				}
			case <-deadline:
				t.Fatalf("got %d of %d pattern events: %+v", len(seen), n, seen)
			}
		}
		return seen
	}
}

// noMorePatterns asserts nothing else was reported.
func noMorePatterns(t *testing.T, collect func(int) []pulse.Event, want int) {
	t.Helper()
	time.Sleep(60 * time.Millisecond)
	if got := len(collect(want)); got != want {
		t.Fatalf("reported %d pattern events, want %d", got, want)
	}
}

const verifierDiscrepancy = "<status>verified-with-corrections</status>\n<questions>\n- q1\n</questions>\n<answers>\n- a1\n</answers>\n<discrepancies>\nq1 contradicts the draft\n</discrepancies>\n<final>\ncorrected answer\n</final>"

// A pattern that is disabled, or whose guards reject the result, did
// nothing: it must not light up. This is why the taps live inside the hooks
// and not in the pipeline wrapper, which runs every hook on every dispatch.
func TestPatternsStaySilentWhenTheyDoNotFire(t *testing.T) {
	collect := watchPatterns(t)
	cfg := Defaults() // refine and verify are opt-in: both off
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "task", Config: cfg}
	cd := &captureDispatch{response: func(workers.AgentCall, int) workers.AgentResult {
		t.Fatal("no dispatch expected")
		return workers.AgentResult{}
	}}
	res := &workers.AgentResult{Output: strings.Repeat("draft ", 80)}
	_ = NewRefineHook(cd.handle, nil).PostRun(context.Background(), hc, res)
	_ = NewVerifyHook(cd.handle, nil).PostRun(context.Background(), hc, res)

	cfg.Refine.Enabled = true
	hc.Config = cfg
	_ = NewRefineHook(cd.handle, nil).PostRun(context.Background(), hc, &workers.AgentResult{Output: "tiny"}) // below MinDraftBytes
	noMorePatterns(t, collect, 0)
}

func TestSelfRefineReportsWhatItConcluded(t *testing.T) {
	collect := watchPatterns(t)
	ctx, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindWorker, Agent: "coder"})
	cfg := Defaults()
	cfg.Refine.Enabled = true
	cfg.Refine.MinDraftBytes = 5
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "SECRET-TASK", Config: cfg}

	rewrite := &captureDispatch{response: func(workers.AgentCall, int) workers.AgentResult {
		return workers.AgentResult{Output: "SECRET-REWRITE better draft"}
	}}
	_ = NewRefineHook(rewrite.handle, nil).PostRun(ctx, hc, &workers.AgentResult{Output: "SECRET-DRAFT original"})

	same := &captureDispatch{response: func(workers.AgentCall, int) workers.AgentResult {
		return workers.AgentResult{Output: "unchanged draft"}
	}}
	_ = NewRefineHook(same.handle, nil).PostRun(ctx, hc, &workers.AgentResult{Output: "unchanged draft"})

	failing := &captureDispatch{response: func(workers.AgentCall, int) workers.AgentResult {
		return workers.AgentResult{Error: errors.New("SECRET-ERROR")}
	}}
	_ = NewRefineHook(failing.handle, nil).PostRun(ctx, hc, &workers.AgentResult{Output: "original draft"})

	evs := collect(6)
	for i, want := range []struct{ status, outcome string }{
		{pulse.StatusOK, outcomeRewrote}, {pulse.StatusOK, outcomeUnchanged}, {pulse.StatusError, outcomeFailed},
	} {
		start, end := evs[2*i], evs[2*i+1]
		if start.Phase != pulse.PhaseStart || start.Name != pulse.PatternSelfRefine || start.Parent != run.ID() {
			t.Fatalf("case %d start = %+v", i, start)
		}
		if end.Phase != pulse.PhaseEnd || end.Status != want.status || end.Attrs["state"] != want.outcome || end.Attrs["passes"] != "1" {
			t.Fatalf("case %d end = %+v, want %s/%s", i, end, want.status, want.outcome)
		}
	}
	wire, _ := json.Marshal(evs)
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("task, draft or error text leaked: %s", wire)
	}
}

func TestCoVeReportsCleanDiscrepancyAndCorrection(t *testing.T) {
	collect := watchPatterns(t)
	cfg := Defaults()
	cfg.Verify.Enabled = true
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "task", Config: cfg}
	answer := func(out string, err error) *captureDispatch {
		return &captureDispatch{response: func(workers.AgentCall, int) workers.AgentResult {
			return workers.AgentResult{Output: out, Error: err}
		}}
	}

	_ = NewVerifyHook(answer("<status>verified</status>\n<final>\nfine\n</final>", nil).handle, nil).
		PostRun(context.Background(), hc, &workers.AgentResult{Output: "draft"})

	cfg.Verify.RewriteOnDiscrepancy = true
	hc.Config = cfg
	_ = NewVerifyHook(answer(verifierDiscrepancy, nil).handle, nil).PostRun(context.Background(), hc, &workers.AgentResult{Output: "draft"})

	cfg.Verify.RewriteOnDiscrepancy = false
	hc.Config = cfg
	_ = NewVerifyHook(answer(verifierDiscrepancy, nil).handle, nil).PostRun(context.Background(), hc, &workers.AgentResult{Output: "draft"})

	_ = NewVerifyHook(answer("", errors.New("boom")).handle, nil).PostRun(context.Background(), hc, &workers.AgentResult{Output: "draft"})

	evs := collect(8)
	want := []string{outcomeClean, outcomeCorrected, outcomeDiscrepancy, outcomeFailed}
	for i, outcome := range want {
		end := evs[2*i+1]
		if end.Name != pulse.PatternCoVe || end.Attrs["state"] != outcome {
			t.Fatalf("case %d = %+v, want %q", i, end, outcome)
		}
	}
	if evs[5].Status != pulse.StatusOK {
		t.Fatal("finding a discrepancy is the pattern working, not a failure of it")
	}
	if evs[7].Status != pulse.StatusError {
		t.Fatal("a verifier that could not run is an error")
	}
	wire, _ := json.Marshal(evs)
	if strings.Contains(string(wire), "contradicts") || strings.Contains(string(wire), "corrected answer") {
		t.Fatalf("verifier output leaked: %s", wire)
	}
}

type fakeEnqueuer struct{ err error }

func (f fakeEnqueuer) Enqueue(context.Context, LessonRequest) error { return f.err }

func TestReflexionReportsTheTriggerAndTheQueueOutcome(t *testing.T) {
	collect := watchPatterns(t)
	ctx, run := runs.NewRegistry(4).Begin(context.Background(), runs.Info{Kind: runs.KindWorker})
	cfg := Defaults()
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "SECRET-TASK", Config: cfg}
	failed := &workers.AgentResult{Output: "SECRET-ATTEMPT", Error: errors.New("SECRET-ERROR")}

	_ = NewReflexionHookWithQueue(fakeEnqueuer{}, nil, nil, nil).PostRun(ctx, hc, failed)
	_ = NewReflexionHookWithQueue(fakeEnqueuer{err: errors.New("disk full")}, nil, nil, nil).PostRun(ctx, hc, failed)
	_ = NewReflexionHookWithQueue(fakeEnqueuer{}, nil, nil, nil).PostRun(ctx, hc, &workers.AgentResult{Output: "all good"}) // no trigger

	evs := collect(2)
	if evs[0].Phase != pulse.PhasePoint || evs[0].Name != pulse.PatternReflexion || evs[0].Parent != run.ID() {
		t.Fatalf("queued = %+v", evs[0])
	}
	if evs[0].Attrs["state"] != outcomeQueued || evs[0].Attrs["trigger"] != "error" {
		t.Fatalf("queued attrs = %+v", evs[0].Attrs)
	}
	if evs[1].Status != pulse.StatusError || evs[1].Attrs["state"] != outcomeQueueFailed {
		t.Fatalf("queue failure = %+v", evs[1])
	}
	noMorePatterns(t, collect, 2)
	wire, _ := json.Marshal(evs)
	if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), "disk full") {
		t.Fatalf("content leaked: %s", wire)
	}
}

func TestReflexionLegacyPathReportsItsBackgroundOutcome(t *testing.T) {
	collect := watchPatterns(t)
	llm := func(context.Context, []models.Message) (string, error) { return "", errors.New("provider down") }
	h := NewReflexionHook(llm, func(context.Context, Lesson) error { return nil }, nil)
	h.runReflexion(context.Background(), LessonRequest{Trigger: "manual", Task: "SECRET-TASK"})

	evs := collect(2)
	if evs[1].Attrs["state"] != outcomeFailed || evs[1].Status != pulse.StatusError || evs[1].Attrs["trigger"] != "manual" {
		t.Fatalf("end = %+v", evs[1])
	}
	if evs[0].Parent != "" {
		t.Fatal("the background half outlives the run: it hangs from the session")
	}
}

func TestReasoningReportsTheEffortItAttached(t *testing.T) {
	collect := watchPatterns(t)
	cfg := ReasoningConfig{Mode: "auto", Budget: 8000, AutoAgents: []string{"planner"}}
	applyAutoReasoning(context.Background(), cfg, &agentWithType{t: "coder"})   // not an auto agent: silent
	applyAutoReasoning(context.Background(), cfg, &agentWithType{t: "planner"}) // fires
	cfg.Mode = "off"
	applyAutoReasoning(context.Background(), cfg, &agentWithType{t: "planner"}) // off: silent

	evs := collect(1)
	if evs[0].Name != pulse.PatternReasoning || evs[0].Attrs["agent"] != "planner" || !strings.HasPrefix(evs[0].Attrs["state"], "effort ") {
		t.Fatalf("reasoning = %+v", evs[0])
	}
	noMorePatterns(t, collect, 1)
}

func TestRefineOutcomeTable(t *testing.T) {
	for _, tc := range []struct {
		failed, rolledBack, changed bool
		status, outcome             string
	}{
		{false, false, true, pulse.StatusOK, outcomeRewrote},
		{false, false, false, pulse.StatusOK, outcomeUnchanged},
		{false, true, true, pulse.StatusOK, outcomeRolledBack},
		{true, false, false, pulse.StatusError, outcomeFailed},
		{true, false, true, pulse.StatusOK, outcomeRewrote}, // an earlier pass already improved the draft
	} {
		status, outcome := refineOutcome(tc.failed, tc.rolledBack, tc.changed)
		if status != tc.status || outcome != tc.outcome {
			t.Errorf("refineOutcome(%v,%v,%v) = %s/%s, want %s/%s", tc.failed, tc.rolledBack, tc.changed, status, outcome, tc.status, tc.outcome)
		}
	}
	endRefine(nil, 1, false, false, true) // nil span: no-op
}
