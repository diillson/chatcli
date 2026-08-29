/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package taskgraph

import (
	"strings"
	"testing"
)

func TestParseGraphEnvelopeWithFencesAndAliases(t *testing.T) {
	raw := "Here is the plan:\n```json\n" + `{
		"name": "feature-x",
		"phases": [{"id":"F1","title":"Server"}],
		"tasks": [
			{"id":"T1","phase":"F1","task":"Implement the endpoint","validation":"tests green"},
			{"id":"T2","description":"Wire the client","deps":["T1"],
			 "validation":[{"run":"go build ./...","expect":"clean"}]}
		]
	}` + "\n```\ntrailing prose"
	g, err := ParseGraph(raw)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	if len(g.Tasks) != 2 {
		t.Fatalf("want 2 tasks, got %d", len(g.Tasks))
	}
	t1 := g.TaskByID("T1")
	if t1.Prompt != "Implement the endpoint" {
		t.Fatalf("task alias not folded into prompt: %q", t1.Prompt)
	}
	if t1.Agent != "coder" {
		t.Fatalf("agent default: got %q", t1.Agent)
	}
	if t1.MaxAttempts != defaultMaxAttempts {
		t.Fatalf("max attempts default: got %d", t1.MaxAttempts)
	}
	if len(t1.Validation) != 1 || t1.Validation[0].Run != "" || t1.Validation[0].Expect != "tests green" {
		t.Fatalf("prose validation not normalized: %+v", t1.Validation)
	}
	t2 := g.TaskByID("T2")
	if t2.Prompt != "Wire the client" {
		t.Fatalf("description alias not folded: %q", t2.Prompt)
	}
	if len(t2.Validation) != 1 || t2.Validation[0].Run != "go build ./..." {
		t.Fatalf("structured validation lost: %+v", t2.Validation)
	}
	if !g.RequiresReview(t1) {
		t.Fatal("review must default to required")
	}
}

func TestParseGraphSingleValidationObject(t *testing.T) {
	g, err := ParseGraph(`{"name":"x","tasks":[{"id":"T1","prompt":"p","validation":{"run":"go vet ./...","expect":"ok"}}]}`)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	if v := g.Tasks[0].Validation; len(v) != 1 || v[0].Run != "go vet ./..." {
		t.Fatalf("single-object validation: %+v", v)
	}
}

func TestParseGraphRejectsForwardAndUnknownDeps(t *testing.T) {
	if _, err := ParseGraph(`{"name":"x","tasks":[{"id":"T1","prompt":"p","deps":["T2"]},{"id":"T2","prompt":"p"}]}`); err == nil {
		t.Fatal("forward dep (cycle-shaped) must be rejected")
	}
	if _, err := ParseGraph(`{"name":"x","tasks":[{"id":"T1","prompt":"p","deps":["NOPE"]}]}`); err == nil {
		t.Fatal("unknown dep must be rejected")
	}
	if _, err := ParseGraph(`{"name":"x","tasks":[]}`); err == nil {
		t.Fatal("empty graph must be rejected")
	}
	if _, err := ParseGraph("no json here"); err == nil {
		t.Fatal("non-JSON must be rejected")
	}
}

func TestParseGraphRejectsUnknownPhase(t *testing.T) {
	_, err := ParseGraph(`{"name":"x","phases":[{"id":"F1"}],"tasks":[{"id":"T1","prompt":"p","phase":"F9"}]}`)
	if err == nil || !strings.Contains(err.Error(), "phase") {
		t.Fatalf("unknown phase must be rejected, got %v", err)
	}
}

func TestRequiresReviewOverrides(t *testing.T) {
	no, yes := false, true
	g := &Graph{RequireReview: &no, Tasks: []*Task{
		{ID: "T1"},
		{ID: "T2", RequireReview: &yes},
	}}
	if g.RequiresReview(g.Tasks[0]) {
		t.Fatal("graph-level waiver must apply")
	}
	if !g.RequiresReview(g.Tasks[1]) {
		t.Fatal("task-level override must win over graph waiver")
	}
}

func TestCountByStatusAndCost(t *testing.T) {
	g := &Graph{Tasks: []*Task{
		{ID: "a", Status: StatusDone, CostUSD: 0.5},
		{ID: "b", Status: StatusFailed, CostUSD: 0.25},
		{ID: "c", Status: StatusDone},
	}}
	counts := g.CountByStatus()
	if counts[StatusDone] != 2 || counts[StatusFailed] != 1 {
		t.Fatalf("counts: %+v", counts)
	}
	if got := g.TotalCostUSD(); got != 0.75 {
		t.Fatalf("total cost: %v", got)
	}
}

func TestParseGraphNormalizesTaskTools(t *testing.T) {
	g, err := ParseGraph(`{"name":"x","tasks":[{"id":"T1","prompt":"p","tools":["@browser"," websearch ","@ask"]}]}`)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	got := g.Tasks[0].Tools
	if len(got) != 2 || got[0] != "@browser" || got[1] != "@websearch" {
		t.Fatalf("task tools not normalized (denylist @ask): %v", got)
	}
}
