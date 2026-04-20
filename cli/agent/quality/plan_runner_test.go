/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/workers"
)

// stubDispatcher returns a canned AgentResult per step id. unknownTask
// is returned when the call references an id with no canned response.
type stubDispatcher struct {
	canned   map[string]workers.AgentResult
	received []workers.AgentCall
	failOn   string // step id whose execution must error
}

func (s *stubDispatcher) Dispatch(_ context.Context, calls []workers.AgentCall) []workers.AgentResult {
	out := make([]workers.AgentResult, 0, len(calls))
	for _, c := range calls {
		s.received = append(s.received, c)
		if c.ID == s.failOn {
			out = append(out, workers.AgentResult{
				CallID: c.ID, Agent: c.Agent, Task: c.Task,
				Error: errors.New("forced failure"),
			})
			continue
		}
		if r, ok := s.canned[c.ID]; ok {
			r.CallID = c.ID
			r.Agent = c.Agent
			r.Task = c.Task
			out = append(out, r)
			continue
		}
		out = append(out, workers.AgentResult{
			CallID: c.ID, Agent: c.Agent, Task: c.Task,
			Output: "ok-" + c.ID,
		})
	}
	return out
}

func TestPlanRunner_LinearChainResolvesPlaceholders(t *testing.T) {
	plan, err := ParsePlan(`{
		"task_summary": "demo",
		"steps": [
			{"id": "E1", "agent": "search", "task": "find foo"},
			{"id": "E2", "agent": "coder", "task": "use #E1"}
		]
	}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	disp := &stubDispatcher{canned: map[string]workers.AgentResult{
		"E1": {Output: "found bar"},
	}}
	res := NewPlanRunner(disp, nil).Execute(context.Background(), plan)

	if res.HadErrors {
		t.Fatalf("expected no errors; report=%s", res.FinalReport)
	}
	if res.StepsExecuted != 2 {
		t.Fatalf("expected 2 steps executed; got %d", res.StepsExecuted)
	}
	if got := disp.received[1].Task; got != "use found bar" {
		t.Fatalf("placeholder not resolved; task=%q", got)
	}
}

func TestPlanRunner_FailedStepDoesNotAbort(t *testing.T) {
	plan, _ := ParsePlan(`{
		"task_summary": "demo",
		"steps": [
			{"id": "E1", "agent": "search", "task": "find foo"},
			{"id": "E2", "agent": "coder", "task": "use #E1"},
			{"id": "E3", "agent": "tester", "task": "verify #E2"}
		]
	}`)

	disp := &stubDispatcher{
		canned: map[string]workers.AgentResult{},
		failOn: "E1",
	}
	res := NewPlanRunner(disp, nil).Execute(context.Background(), plan)

	if !res.HadErrors {
		t.Fatal("expected HadErrors=true after E1 failure")
	}
	if res.StepsExecuted != 3 {
		t.Fatalf("downstream steps should still run; got executed=%d", res.StepsExecuted)
	}
	// E2 should have substituted "<error: …>" for #E1.
	if !strings.Contains(disp.received[1].Task, "<error:") {
		t.Fatalf("E2 should see error substitution; got %q", disp.received[1].Task)
	}
}

func TestPlanRunner_RunFromPlannerOutputBadJSONErrors(t *testing.T) {
	disp := &stubDispatcher{}
	if _, err := NewPlanRunner(disp, nil).RunFromPlannerOutput(context.Background(), "not json"); err == nil {
		t.Fatal("expected parse error")
	}
	if len(disp.received) != 0 {
		t.Fatal("dispatcher must not be called when parse fails")
	}
}

func TestPlanRunner_RunFromPlannerOutputHappyPath(t *testing.T) {
	raw := `{"task_summary":"x","steps":[{"id":"E1","agent":"search","task":"x"}]}`
	disp := &stubDispatcher{canned: map[string]workers.AgentResult{"E1": {Output: "found"}}}
	res, err := NewPlanRunner(disp, nil).RunFromPlannerOutput(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.HadErrors || res.StepsExecuted != 1 {
		t.Fatalf("unexpected run state: %+v", res)
	}
	if !strings.Contains(res.FinalReport, "Plan Execution Report") {
		t.Errorf("report missing header: %s", res.FinalReport)
	}
}

func TestPlanRunner_NilPlanReturnsEmptyResult(t *testing.T) {
	disp := &stubDispatcher{}
	res := NewPlanRunner(disp, nil).Execute(context.Background(), nil)
	if res == nil {
		t.Fatal("must not return nil")
	}
	if res.StepsExecuted != 0 || res.HadErrors {
		t.Fatalf("unexpected state for nil plan: %+v", res)
	}
}
