/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/taskgraph"
	"go.uber.org/zap"
)

func newTestTaskGraphAdapter(t *testing.T) *taskGraphAdapter {
	t.Helper()
	base := t.TempDir()
	a := newTaskGraphAdapter(&ChatCLI{}, zap.NewNop())
	a.baseDirFn = func() (string, error) { return base, nil }
	return a
}

const testPlanJSON = `{"name":"feature-x","tasks":[{"id":"T1","prompt":"do it"}]}`

func TestTaskGraphAdapterPlanStatusShowList(t *testing.T) {
	a := newTestTaskGraphAdapter(t)

	out, err := a.Plan(testPlanJSON)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !strings.Contains(out, "tg-") {
		t.Fatalf("Plan must return the run id: %q", out)
	}

	status, err := a.Status("")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(status, "[T1]") || !strings.Contains(status, "status=pending") {
		t.Fatalf("Status output: %q", status)
	}

	show, err := a.Show("", "T1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !strings.Contains(show, "prompt: do it") {
		t.Fatalf("Show output: %q", show)
	}
	if _, err := a.Show("", "T9"); err == nil {
		t.Fatal("Show of unknown task must error")
	}

	list, err := a.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(list, "feature-x") {
		t.Fatalf("List output: %q", list)
	}

	if _, err := a.Plan("not json"); err == nil {
		t.Fatal("Plan with invalid JSON must error")
	}
}

func TestTaskGraphAdapterErrorsWithoutRunsOrDispatcher(t *testing.T) {
	a := newTestTaskGraphAdapter(t)
	if _, err := a.Status(""); err == nil {
		t.Fatal("Status with no runs must error")
	}
	if _, err := a.Cancel(); err == nil {
		t.Fatal("Cancel with no active run must error")
	}
	// A run attempt without a live agent dispatcher must fail cleanly.
	if _, err := a.Plan(testPlanJSON); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := a.Run(context.Background(), "", "", nil); err == nil || !strings.Contains(err.Error(), "dispatcher") {
		t.Fatalf("Run without dispatcher must name the problem, got %v", err)
	}
	if _, err := a.Retry(context.Background(), "", "T1", nil); err == nil {
		t.Fatal("Retry of a pending task must error")
	}
}

func TestTaskGraphAdapterRetryValidation(t *testing.T) {
	a := newTestTaskGraphAdapter(t)
	if _, err := a.Plan(testPlanJSON); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := a.Retry(context.Background(), "", "T9", nil); err == nil {
		t.Fatal("Retry of unknown task must error")
	}
}

func TestTaskGraphMaxParallelEnv(t *testing.T) {
	t.Setenv(agentMaxWorkersEnv, "7")
	if got := taskGraphMaxParallel(); got != 7 {
		t.Fatalf("env cap: %d", got)
	}
	t.Setenv(agentMaxWorkersEnv, "bogus")
	if got := taskGraphMaxParallel(); got != taskgraph.DefaultMaxParallel {
		t.Fatalf("bogus env must fall back to default: %d", got)
	}
}

func TestCoderGateRunnerCapturesOutputAndFailure(t *testing.T) {
	r := &coderGateRunner{workspace: t.TempDir()}
	out, err := r.RunGateStep(context.Background(), t.TempDir(), "echo hello-gate", nil)
	if err != nil {
		t.Fatalf("RunGateStep: %v", err)
	}
	if !strings.Contains(out, "hello-gate") {
		t.Fatalf("gate output not captured: %q", out)
	}
	if _, err := r.RunGateStep(context.Background(), t.TempDir(), "exit 3", nil); err == nil {
		t.Fatal("non-zero exit must be an error")
	}
}

func TestHandleTaskGraphCommandFlows(t *testing.T) {
	cli := &ChatCLI{}
	// Nil adapter: must not panic, prints the unavailable notice.
	cli.handleTaskGraphCommand("/taskgraph status")

	a := newTestTaskGraphAdapter(t)
	if _, err := a.Plan(testPlanJSON); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	cli.taskGraphAdapter = a
	cli.handleTaskGraphCommand("/taskgraph")
	cli.handleTaskGraphCommand("/taskgraph status")
	cli.handleTaskGraphCommand("/taskgraph list")
	cli.handleTaskGraphCommand("/taskgraph show T1")
	cli.handleTaskGraphCommand("/taskgraph show")
	cli.handleTaskGraphCommand("/taskgraph help")
	cli.handleTaskGraphCommand("/taskgraph bogus")
}

// passDispatcher approves every executor and reviewer call.
type passDispatcher struct{}

func (passDispatcher) Dispatch(_ context.Context, calls []workers.AgentCall) []workers.AgentResult {
	out := make([]workers.AgentResult, 0, len(calls))
	for _, c := range calls {
		res := workers.AgentResult{CallID: c.ID, Agent: c.Agent, Output: "done\nVERDICT: PASS — verified"}
		res.SetMetadata("run_id", "run-"+c.ID)
		out = append(out, res)
	}
	return out
}
func (passDispatcher) SetCallUsageRecorder(workers.CallUsageRecorder) {}

func TestTaskGraphAdapterRunEndToEnd(t *testing.T) {
	a := newTestTaskGraphAdapter(t)
	a.dispatcherFn = func() (taskGraphDispatcher, error) { return passDispatcher{}, nil }

	var streamed strings.Builder
	report, err := a.Run(context.Background(),
		"", `{"name":"e2e","tasks":[{"id":"T1","prompt":"one"},{"id":"T2","prompt":"two","deps":["T1"]}]}`,
		func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(report, "status=done") {
		t.Fatalf("report: %q", report)
	}
	if !strings.Contains(streamed.String(), "task_done") {
		t.Fatalf("events must stream: %q", streamed.String())
	}
	// Running the same (finished) run again returns the report directly.
	again, err := a.Run(context.Background(), "", "", nil)
	if err != nil || !strings.Contains(again, "status=done") {
		t.Fatalf("re-run of done graph: %q %v", again, err)
	}
	// A finished run has nothing to cancel.
	if _, err := a.Cancel(); err == nil {
		t.Fatal("Cancel after completion must error")
	}
}

func TestTaskGraphAdapterStatusByExplicitID(t *testing.T) {
	a := newTestTaskGraphAdapter(t)
	if _, err := a.Plan(testPlanJSON); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	rows, err := taskgraph.ListRuns(mustBaseDir(t, a))
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListRuns: %v %v", rows, err)
	}
	if _, err := a.Status(rows[0].RunID); err != nil {
		t.Fatalf("Status by id: %v", err)
	}
	if _, err := a.Status("tg-missing"); err == nil {
		t.Fatal("Status of unknown id must error")
	}
}

func mustBaseDir(t *testing.T, a *taskGraphAdapter) string {
	t.Helper()
	base, err := a.baseDir()
	if err != nil {
		t.Fatalf("baseDir: %v", err)
	}
	return base
}

func TestTaskGraphAdapterDashServesAndReuses(t *testing.T) {
	a := newTestTaskGraphAdapter(t)
	if _, err := a.Plan(testPlanJSON); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out1, err := a.Dash("")
	if err != nil {
		t.Fatalf("Dash: %v", err)
	}
	if !strings.Contains(out1, "http://127.0.0.1:") {
		t.Fatalf("Dash must return the local URL: %q", out1)
	}
	out2, err := a.Dash("tg-something")
	if err != nil {
		t.Fatalf("Dash 2: %v", err)
	}
	if !strings.Contains(out2, "?run=tg-something") {
		t.Fatalf("run focus must land in the URL: %q", out2)
	}
	u1 := out1[strings.Index(out1, "http") : strings.Index(out1, "/ ")+1]
	if !strings.Contains(out2, u1[:strings.LastIndex(u1, "/")]) {
		t.Fatalf("second Dash must reuse the same server: %q vs %q", out1, out2)
	}
	a.shutdownDash(context.Background())
	a.shutdownDash(context.Background()) // idempotent
}

func TestParseRetention(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", taskgraph.DefaultRetention},
		{"all", 0},
		{"7d", 7 * 24 * time.Hour},
		{"72h", 72 * time.Hour},
	}
	for _, c := range cases {
		got, err := parseRetention(c.in)
		if err != nil || got != c.want {
			t.Fatalf("parseRetention(%q) = %v, %v", c.in, got, err)
		}
	}
	for _, bad := range []string{"bogus", "-2h", "0d"} {
		if _, err := parseRetention(bad); err == nil {
			t.Fatalf("parseRetention(%q) must error", bad)
		}
	}
}

func TestTaskGraphAdapterPrune(t *testing.T) {
	a := newTestTaskGraphAdapter(t)
	if _, err := a.Plan(testPlanJSON); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out, err := a.Prune("all")
	if err != nil || !strings.Contains(out, "pruned 1 run(s)") {
		t.Fatalf("Prune: %q %v", out, err)
	}
	if _, err := a.Prune("bogus"); err == nil {
		t.Fatal("invalid retention must error")
	}
}
