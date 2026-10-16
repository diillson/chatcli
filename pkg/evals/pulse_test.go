/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package evals

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// parentJudge grades every request 0.9 and records the dashboard parent
// each one carried.
type parentJudge struct {
	mu      sync.Mutex
	parents []string
}

func (j *parentJudge) Grade(ctx context.Context, _ JudgeRequest) (JudgeVerdict, error) {
	j.mu.Lock()
	j.parents = append(j.parents, pulse.ParentFrom(ctx))
	j.mu.Unlock()
	return JudgeVerdict{Score: 0.9, CostUSD: 0.01}, nil
}

func TestRunShowsEachTrialOnDashboard(t *testing.T) {
	rec := pulsetest.Record(t)
	ex := newFakeExec(map[string][]fakeRun{
		"secret-case": {{final: "SECRET-ANSWER yes", cost: 0.02}, {final: "no"}},
	})
	judge := &parentJudge{}
	c := mkCase("secret-case", PassAll, 2,
		Check{Contains: "yes"},
		Check{Judge: &JudgeCheck{Rubric: "SECRET-RUBRIC", Threshold: 0.5, Samples: 1}})
	Run(context.Background(), []*Suite{{Name: "s", Cases: []*Case{c}}}, Options{Executor: ex, Judge: judge, Concurrency: 1})

	ends := func() []pulse.Event {
		return rec.Match(pulsetest.Is(pulse.KindBackground, pulseCase, pulse.PhaseEnd))
	}
	rec.WaitFor(t, "second trial end", func(pulse.Event) bool { return len(ends()) >= 2 })
	byTrial := map[string]pulse.Event{}
	for _, ev := range ends() {
		byTrial[ev.Attrs["trial"]] = ev
	}
	pass, fail := byTrial["1"], byTrial["2"]
	if pass.Attrs["passed"] != "true" || pass.Attrs["state"] != StatusPass || pass.Attrs["score"] != "0.95" ||
		pass.Attrs["mode"] != ModeChat || pass.Attrs["checks"] != "2" || pass.Attrs["checks_passed"] != "2" ||
		pass.Attrs["cost_usd"] != "0.0300" || pass.Status != pulse.StatusOK {
		t.Errorf("passing trial = %+v", pass)
	}
	if fail.Attrs["passed"] != "false" || fail.Attrs["state"] != StatusFail || fail.Attrs["checks_passed"] != "1" {
		t.Errorf("failing trial = %+v", fail)
	}

	starts := rec.Match(pulsetest.Is(pulse.KindBackground, pulseCase, pulse.PhaseStart))
	ids := map[string]bool{}
	for _, s := range starts {
		ids[s.ID] = true
	}
	if len(judge.parents) != 2 {
		t.Fatalf("judge calls = %d", len(judge.parents))
	}
	for _, p := range judge.parents {
		if !ids[p] {
			t.Errorf("judge call hangs from %q, not from a trial node %v", p, ids)
		}
	}
	for _, e := range rec.Events() {
		if strings.Contains(e.Name, "secret") {
			t.Fatalf("case id leaked into a node name: %q", e.Name)
		}
		for _, v := range e.Attrs {
			if strings.Contains(strings.ToLower(v), "secret") {
				t.Fatalf("case content leaked onto the bus: %q", v)
			}
		}
	}
}

func TestRunSkippedTrialsShowAsCancelled(t *testing.T) {
	rec := pulsetest.Record(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex := newFakeExec(map[string][]fakeRun{"a": {{final: "y"}}})
	Run(ctx, []*Suite{{Name: "s", Cases: []*Case{mkCase("a", PassAll, 1, Check{Contains: "y"})}}}, Options{Executor: ex})
	ev := rec.WaitFor(t, "skipped trial", pulsetest.Is(pulse.KindBackground, pulseCase, pulse.PhasePoint))
	if ev.Status != pulse.StatusCancelled || ev.Attrs["reason"] != "canceled" || ev.Attrs["state"] != StatusSkipped {
		t.Errorf("skipped = %+v", ev)
	}
}
