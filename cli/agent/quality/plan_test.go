/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePlan_StripFenceAndExtractJSON(t *testing.T) {
	raw := "Here is the plan:\n\n```json\n" + `{
  "task_summary": "x",
  "steps": [
    {"id": "E1", "agent": "search", "task": "find foo"}
  ]
}` + "\n```\nDone."
	p, err := ParsePlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.TaskSummary != "x" || len(p.Steps) != 1 || p.Steps[0].ID != "E1" {
		t.Fatalf("plan parsed wrong: %+v", p)
	}
}

func TestParsePlan_RejectsEmptySteps(t *testing.T) {
	if _, err := ParsePlan(`{"task_summary":"x","steps":[]}`); err == nil {
		t.Fatal("expected error on empty steps")
	}
}

func TestParsePlan_RejectsForwardDep(t *testing.T) {
	raw := `{
		"task_summary": "x",
		"steps": [
			{"id": "E1", "agent": "search", "task": "x", "deps": ["E2"]},
			{"id": "E2", "agent": "coder", "task": "y"}
		]
	}`
	if _, err := ParsePlan(raw); err == nil {
		t.Fatal("expected error on forward dep")
	}
}

func TestParsePlan_RejectsUnknownDep(t *testing.T) {
	raw := `{
		"task_summary": "x",
		"steps": [
			{"id": "E1", "agent": "search", "task": "x", "deps": ["E9"]}
		]
	}`
	if _, err := ParsePlan(raw); err == nil {
		t.Fatal("expected error on unknown dep")
	}
}

func TestParsePlan_RejectsDuplicateIDs(t *testing.T) {
	raw := `{
		"task_summary": "x",
		"steps": [
			{"id": "E1", "agent": "search", "task": "x"},
			{"id": "E1", "agent": "coder", "task": "y"}
		]
	}`
	if _, err := ParsePlan(raw); err == nil {
		t.Fatal("expected error on duplicate id")
	}
}

func TestParsePlan_RejectsUnknownPlaceholder(t *testing.T) {
	raw := `{
		"task_summary": "x",
		"steps": [
			{"id": "E1", "agent": "search", "task": "use #E9 result"}
		]
	}`
	if _, err := ParsePlan(raw); err == nil {
		t.Fatal("expected error on unknown placeholder reference")
	}
}

func TestPlan_TopologicalOrder_LinearChain(t *testing.T) {
	p := &Plan{Steps: []*PlanStep{
		{ID: "E1", Agent: "a", Task: "t"},
		{ID: "E2", Agent: "a", Task: "t", Deps: []string{"E1"}},
		{ID: "E3", Agent: "a", Task: "t", Deps: []string{"E2"}},
	}}
	order := p.TopologicalOrder()
	want := []string{"E1", "E2", "E3"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("topo order mismatch: got %v want %v", order, want)
	}
}

func TestPlan_TopologicalOrder_FanOut(t *testing.T) {
	p := &Plan{Steps: []*PlanStep{
		{ID: "E1", Agent: "a", Task: "t"},
		{ID: "E2", Agent: "a", Task: "t", Deps: []string{"E1"}},
		{ID: "E3", Agent: "a", Task: "t", Deps: []string{"E1"}},
		{ID: "E4", Agent: "a", Task: "t", Deps: []string{"E2", "E3"}},
	}}
	order := p.TopologicalOrder()
	if len(order) != 4 || order[0] != "E1" || order[3] != "E4" {
		t.Fatalf("fan-out topo broken: %v", order)
	}
}

func TestResolvePlaceholders(t *testing.T) {
	outputs := map[string]string{
		"E1": "first line\nsecond line",
		"E2": "hello world",
	}
	cases := map[string]string{
		"use #E1":         "use first line\nsecond line",
		"use #E1.summary": "use first line",
		"use #E1.head=5":  "use first…",
		"use #E2.last=5":  "use …world",
		"use #E1 and #E2": "use first line\nsecond line and hello world",
		"unknown #E9":     "unknown #E9", // left intact
	}
	for in, want := range cases {
		got := ResolvePlaceholders(in, outputs)
		if got != want {
			t.Errorf("input=%q got=%q want=%q", in, got, want)
		}
	}
}

func TestComplexityScore_TrivialTask(t *testing.T) {
	if s := ComplexityScore("read main.go"); s > 3 {
		t.Errorf("trivial task scored too high: %d", s)
	}
}

func TestComplexityScore_MultiActionMultiFileExceedsThreshold(t *testing.T) {
	task := "update auth.go and add tests/auth_test.go then run go test"
	if s := ComplexityScore(task); s < 6 {
		t.Errorf("multi-action multi-file task scored too low: %d", s)
	}
}

func TestComplexityScore_PortugueseTriggersToo(t *testing.T) {
	task := "criar handler em api.go e adicionar testes em api_test.go depois rodar go test"
	if s := ComplexityScore(task); s < 6 {
		t.Errorf("PT multi-action task scored too low: %d", s)
	}
}

func TestShouldPlanFirst(t *testing.T) {
	cfg := PlanFirstConfig{Mode: "auto", ComplexityThreshold: 6}
	if ShouldPlanFirst(cfg, "ls files") {
		t.Error("simple task should not trigger plan-first under auto")
	}
	cfg.Mode = "always"
	if !ShouldPlanFirst(cfg, "ls files") {
		t.Error("always mode must trigger regardless of complexity")
	}
	cfg.Mode = "off"
	if ShouldPlanFirst(cfg, "complex multi-file refactor task") {
		t.Error("off mode must never trigger")
	}
}

func TestParsePlan_StripsBackticks(t *testing.T) {
	raw := "```\n" + `{"task_summary":"x","steps":[{"id":"E1","agent":"search","task":"x"}]}` + "\n```"
	if _, err := ParsePlan(raw); err != nil {
		t.Fatalf("backtick fenced JSON should parse: %v", err)
	}
}

func TestParsePlan_EmptyInput(t *testing.T) {
	if _, err := ParsePlan(""); err == nil {
		t.Fatal("empty input must error")
	}
	if _, err := ParsePlan("no json here"); err == nil {
		t.Fatal("no-json input must error")
	}
}

func TestPlanStep_ToAgentCall(t *testing.T) {
	s := &PlanStep{ID: "E1", Agent: "  Coder  ", Task: "x"}
	call := s.ToAgentCall("resolved")
	if string(call.Agent) != "coder" {
		t.Errorf("agent name should be lower-cased and trimmed; got %q", call.Agent)
	}
	if call.Task != "resolved" || call.ID != "E1" {
		t.Errorf("call mismatch: %+v", call)
	}
}

func TestParsePlan_TolerantToTrailingProse(t *testing.T) {
	raw := `Sure, here is your plan: {"task_summary":"x","steps":[{"id":"E1","agent":"search","task":"x"}]} thanks!`
	p, err := ParsePlan(raw)
	if err != nil {
		t.Fatalf("trailing prose should be tolerated: %v", err)
	}
	if !strings.EqualFold(p.Steps[0].ID, "E1") {
		t.Fatalf("plan parsed wrong: %+v", p)
	}
}
