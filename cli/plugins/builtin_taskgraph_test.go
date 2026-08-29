/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseTaskGraphInvocationEnvelope(t *testing.T) {
	sub, payload, err := parseTaskGraphInvocation([]string{`{"cmd":"show","args":{"task":"T3","id":"tg-1"}}`})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sub != "show" || jsonString(payload, "task") != "T3" || jsonString(payload, "id") != "tg-1" {
		t.Fatalf("sub=%q payload=%v", sub, payload)
	}
}

func TestParseTaskGraphInvocationFlatPlanMeansRun(t *testing.T) {
	sub, payload, err := parseTaskGraphInvocation([]string{`{"name":"x","tasks":[{"id":"T1","prompt":"p"}]}`})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sub != "run" {
		t.Fatalf("flat plan payload must default to run, got %q", sub)
	}
	if taskGraphPlanJSON(payload) == "" {
		t.Fatal("plan JSON must be recoverable from the flat payload")
	}
}

func TestParseTaskGraphInvocationFlatArgsRegression(t *testing.T) {
	// The agent loop flattens {"cmd":"show","args":{"task":"T3"}} into
	// ["show","--task","T3"] — the flag name must never become the value
	// (regression class of PR #1407).
	sub, payload, err := parseTaskGraphInvocation([]string{"show", "--task", "T3"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sub != "show" || jsonString(payload, "task") != "T3" {
		t.Fatalf("flattened flags mishandled: sub=%q payload=%v", sub, payload)
	}
	sub, payload, err = parseTaskGraphInvocation([]string{"run", "--id=tg-42"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sub != "run" || jsonString(payload, "id") != "tg-42" {
		t.Fatalf("--key=value mishandled: sub=%q payload=%v", sub, payload)
	}
}

func TestParseTaskGraphInvocationPositionals(t *testing.T) {
	sub, payload, err := parseTaskGraphInvocation([]string{"retry", "T3"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sub != "retry" || jsonString(payload, "task") != "T3" {
		t.Fatalf("positional task id: sub=%q payload=%v", sub, payload)
	}
	sub, payload, err = parseTaskGraphInvocation([]string{"status", "tg-20260829-1"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sub != "status" || jsonString(payload, "id") != "tg-20260829-1" {
		t.Fatalf("positional run id: sub=%q payload=%v", sub, payload)
	}
	if sub, _, _ := parseTaskGraphInvocation(nil); sub != "status" {
		t.Fatalf("no args must default to status, got %q", sub)
	}
}

func TestTaskGraphPlanJSONUnwrapsStringValue(t *testing.T) {
	payload := map[string]json.RawMessage{
		"graph": json.RawMessage(`"{\"name\":\"x\",\"tasks\":[]}"`),
	}
	if got := taskGraphPlanJSON(payload); !strings.HasPrefix(got, `{"name"`) {
		t.Fatalf("JSON-in-a-string not unwrapped: %q", got)
	}
}

func TestTaskGraphCaps(t *testing.T) {
	p := NewBuiltinTaskGraphPlugin()
	readOnly := [][]string{
		{`{"cmd":"status"}`},
		{`{"cmd":"show","args":{"task":"T1"}}`},
		{"list"},
	}
	for _, args := range readOnly {
		if !p.IsReadOnly(args) || !p.IsConcurrencySafe(args) {
			t.Fatalf("args %v must be read-only + concurrency-safe", args)
		}
	}
	mutating := [][]string{
		{`{"cmd":"run"}`},
		{`{"cmd":"plan","args":{"graph":{}}}`},
		{"retry", "T3"},
		{`{"cmd":"cancel"}`},
		{`{"name":"x","tasks":[{"id":"T1","prompt":"p"}]}`}, // flat plan → run
	}
	for _, args := range mutating {
		if p.IsReadOnly(args) || p.IsConcurrencySafe(args) {
			t.Fatalf("args %v must be treated as mutating (fail closed)", args)
		}
	}
	if p.DescribeCall([]string{`{"cmd":"run"}`}) == "" {
		t.Fatal("DescribeCall must never be empty")
	}
}

// recordingTaskGraphAdapter records which method ran and with what.
type recordingTaskGraphAdapter struct {
	method, runID, graphJSON, taskID string
}

func (r *recordingTaskGraphAdapter) Plan(graphJSON string) (string, error) {
	r.method, r.graphJSON = "plan", graphJSON
	return "ok", nil
}
func (r *recordingTaskGraphAdapter) Run(_ context.Context, runID, graphJSON string, _ func(string)) (string, error) {
	r.method, r.runID, r.graphJSON = "run", runID, graphJSON
	return "ok", nil
}
func (r *recordingTaskGraphAdapter) Status(runID string) (string, error) {
	r.method, r.runID = "status", runID
	return "ok", nil
}
func (r *recordingTaskGraphAdapter) Show(runID, taskID string) (string, error) {
	r.method, r.runID, r.taskID = "show", runID, taskID
	return "ok", nil
}
func (r *recordingTaskGraphAdapter) Retry(_ context.Context, runID, taskID string, _ func(string)) (string, error) {
	r.method, r.runID, r.taskID = "retry", runID, taskID
	return "ok", nil
}
func (r *recordingTaskGraphAdapter) Cancel() (string, error) { r.method = "cancel"; return "ok", nil }
func (r *recordingTaskGraphAdapter) List() (string, error)   { r.method = "list"; return "ok", nil }

func TestTaskGraphExecuteDispatch(t *testing.T) {
	rec := &recordingTaskGraphAdapter{}
	SetTaskGraphAdapter(rec)
	t.Cleanup(func() { SetTaskGraphAdapter(nil) })
	p := NewBuiltinTaskGraphPlugin()

	cases := []struct {
		args   []string
		method string
	}{
		{[]string{`{"cmd":"plan","args":{"graph":{"name":"x","tasks":[]}}}`}, "plan"},
		{[]string{`{"cmd":"run","args":{"id":"tg-1"}}`}, "run"},
		{[]string{`{"cmd":"status"}`}, "status"},
		{[]string{"show", "--task", "T3"}, "show"},
		{[]string{"retry", "T3"}, "retry"},
		{[]string{`{"cmd":"cancel"}`}, "cancel"},
		{[]string{"list"}, "list"},
	}
	for _, c := range cases {
		if _, err := p.Execute(context.Background(), c.args); err != nil {
			t.Fatalf("Execute(%v): %v", c.args, err)
		}
		if rec.method != c.method {
			t.Fatalf("Execute(%v) routed to %q, want %q", c.args, rec.method, c.method)
		}
	}
	if rec.taskID != "T3" {
		t.Fatalf("task id not threaded: %q", rec.taskID)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"show"}`}); err == nil {
		t.Fatal("show without task must error")
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"nope"}`}); err == nil {
		t.Fatal("unknown subcommand must error")
	}
}

func TestTaskGraphExecuteWithoutAdapter(t *testing.T) {
	SetTaskGraphAdapter(nil)
	p := NewBuiltinTaskGraphPlugin()
	if _, err := p.Execute(context.Background(), []string{"status"}); err == nil {
		t.Fatal("no adapter must be an explicit error")
	}
}

func TestTaskGraphSchemaSurface(t *testing.T) {
	var schema struct {
		ArgsFormat  string `json:"argsFormat"`
		Subcommands []struct {
			Name string `json:"name"`
		} `json:"subcommands"`
	}
	if err := json.Unmarshal([]byte(NewBuiltinTaskGraphPlugin().Schema()), &schema); err != nil {
		t.Fatalf("Schema must be valid JSON: %v", err)
	}
	if schema.ArgsFormat == "" || len(schema.Subcommands) < 6 {
		t.Fatalf("schema must declare argsFormat and the subcommands: %+v", schema)
	}
}

func TestTaskGraphPluginIdentity(t *testing.T) {
	p := NewBuiltinTaskGraphPlugin()
	if p.Name() != "@taskgraph" || p.Version() == "" {
		t.Fatalf("identity: %q %q", p.Name(), p.Version())
	}
	if p.Path() != "" {
		t.Fatalf("Path must be empty (catalog-deferred), got %q", p.Path())
	}
	if !strings.Contains(p.Description(), "reviewer") || !strings.Contains(p.Usage(), "run") {
		t.Fatal("description/usage must describe the review contract and subcommands")
	}
}

// dashRecordingAdapter layers the optional dashboard capability on top of
// the recording adapter.
type dashRecordingAdapter struct {
	recordingTaskGraphAdapter
	dashRunID string
}

func (d *dashRecordingAdapter) Dash(runID string) (string, error) {
	d.dashRunID, d.method = runID, "dash"
	return "http://127.0.0.1:1/", nil
}

func TestTaskGraphDashCapability(t *testing.T) {
	// Base adapter without the capability: explicit refusal.
	SetTaskGraphAdapter(&recordingTaskGraphAdapter{})
	t.Cleanup(func() { SetTaskGraphAdapter(nil) })
	p := NewBuiltinTaskGraphPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"dash"}`}); err == nil {
		t.Fatal("dash without the capability must error")
	}
	// With the capability: routed, run id threaded.
	rec := &dashRecordingAdapter{}
	SetTaskGraphAdapter(rec)
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"dash","args":{"id":"tg-9"}}`}); err != nil {
		t.Fatalf("dash: %v", err)
	}
	if rec.method != "dash" || rec.dashRunID != "tg-9" {
		t.Fatalf("dash routing: %q %q", rec.method, rec.dashRunID)
	}
	if p.IsReadOnly([]string{`{"cmd":"dash"}`}) {
		t.Fatal("dash starts a server and opens a browser — never read-only")
	}
}
