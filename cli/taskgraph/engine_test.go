/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package taskgraph

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/models"
)

// fakeDispatcher scripts executor and reviewer results per call-ID prefix
// and records dispatch order for dependency assertions.
type fakeDispatcher struct {
	mu    sync.Mutex
	order []string
	// failExecutorOnce / failReviewOnce hold call IDs that must fail once.
	failExecutorOnce map[string]bool
	reviewFailOnce   map[string]bool
	reviewOutput     string
	grants           map[string][]string
}

func newFakeDispatcher() *fakeDispatcher {
	return &fakeDispatcher{
		failExecutorOnce: map[string]bool{},
		reviewFailOnce:   map[string]bool{},
		reviewOutput:     "checked the workspace\nVERDICT: PASS — evidence recorded",
	}
}

func (f *fakeDispatcher) Dispatch(_ context.Context, calls []workers.AgentCall) []workers.AgentResult {
	out := make([]workers.AgentResult, 0, len(calls))
	for _, c := range calls {
		f.mu.Lock()
		f.order = append(f.order, c.ID)
		if c.Plugins != nil {
			if f.grants == nil {
				f.grants = map[string][]string{}
			}
			f.grants[c.ID] = c.Plugins.Plugins
		}
		execFail := f.failExecutorOnce[c.ID]
		delete(f.failExecutorOnce, c.ID)
		reviewFail := f.reviewFailOnce[c.ID]
		delete(f.reviewFailOnce, c.ID)
		reviewOut := f.reviewOutput
		f.mu.Unlock()

		res := workers.AgentResult{CallID: c.ID, Agent: c.Agent, Task: c.Task}
		switch {
		case execFail:
			res.Error = errors.New("scripted executor failure")
		case reviewFail:
			res.Output = "the tests are missing\nVERDICT: FAIL — no test covers the endpoint"
		case c.Agent == workers.AgentTypeReviewer:
			res.Output = reviewOut
		default:
			res.Output = "did the work for " + c.ID
		}
		res.SetMetadata("run_id", "run-"+c.ID)
		out = append(out, res)
	}
	return out
}

func (f *fakeDispatcher) callOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.order...)
}

// fakeGate scripts gate outcomes per command substring.
type fakeGate struct {
	mu       sync.Mutex
	failOnce map[string]bool
	ran      []string
}

func (f *fakeGate) RunGateStep(_ context.Context, _, cmd string, _ func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, cmd)
	if f.failOnce[cmd] {
		delete(f.failOnce, cmd)
		return "FAIL: 1 test failed", errors.New("exit status 1")
	}
	return "ok\nPASS", nil
}

func testGraph(t *testing.T, raw string) (*Graph, *RunStore) {
	t.Helper()
	g, err := ParseGraph(raw)
	if err != nil {
		t.Fatalf("ParseGraph: %v", err)
	}
	store, err := CreateRun(t.TempDir(), g)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return g, store
}

func newTestEngine(t *testing.T, g *Graph, store *RunStore, disp StepDispatcher, gate GateRunner) *Engine {
	t.Helper()
	e, err := NewEngine(g, store, Config{Dispatcher: disp, Gate: gate, Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestEngineHappyPathRespectsDepsAndReview(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","tasks":[
		{"id":"T1","prompt":"first","validation":[{"run":"go test ./...","expect":"green"}]},
		{"id":"T2","prompt":"second uses #T1","deps":["T1"]}
	]}`)
	disp := newFakeDispatcher()
	gate := &fakeGate{}
	e := newTestEngine(t, g, store, disp, gate)

	report, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if g.Status != StatusDone {
		t.Fatalf("graph status: %s (report: %s)", g.Status, report)
	}
	for _, id := range []string{"T1", "T2"} {
		task := g.TaskByID(id)
		if task.Status != StatusDone {
			t.Fatalf("task %s: %s", id, task.Status)
		}
		last := task.Attempts[len(task.Attempts)-1]
		if last.Verdict != verdictPass {
			t.Fatalf("task %s verdict: %q", id, last.Verdict)
		}
		if last.ExecutorRunID == "" || last.ReviewerRunID == "" || last.ExecutorRunID == last.ReviewerRunID {
			t.Fatalf("executor/reviewer must be distinct runs: %+v", last)
		}
	}
	order := disp.callOrder()
	idx := func(s string) int {
		for i, v := range order {
			if v == s {
				return i
			}
		}
		return -1
	}
	if !(idx("tg:T1:e1") < idx("tg:T1:r1") && idx("tg:T1:r1") < idx("tg:T2:e1")) {
		t.Fatalf("dependency/review ordering violated: %v", order)
	}
	if len(gate.ran) != 1 || gate.ran[0] != "go test ./..." {
		t.Fatalf("gate must run exactly the contract command: %v", gate.ran)
	}
	// The stored plan keeps the #T1 reference (substitution happens on the
	// dispatched prompt, never destructively on the plan).
	if t2Prompt := g.TaskByID("T2").Prompt; !strings.Contains(t2Prompt, "#T1") {
		t.Fatalf("stored prompt must keep the reference: %q", t2Prompt)
	}
}

func TestEngineGateFailureRetriesWithFeedback(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","tasks":[
		{"id":"T1","prompt":"work","validation":[{"run":"go test ./...","expect":"green"}]}
	]}`)
	disp := newFakeDispatcher()
	gate := &fakeGate{failOnce: map[string]bool{"go test ./...": true}}
	e := newTestEngine(t, g, store, disp, gate)

	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task := g.TaskByID("T1")
	if task.Status != StatusDone {
		t.Fatalf("task must recover on retry, got %s", task.Status)
	}
	if len(task.Attempts) != 2 {
		t.Fatalf("want 2 attempts, got %d", len(task.Attempts))
	}
	if task.Attempts[0].FailureReason == "" || !task.Attempts[0].Gate[0].Passed == false {
		t.Fatalf("first attempt must record the gate failure: %+v", task.Attempts[0])
	}
}

func TestEngineReviewFailThenExhaustionBlocksSuccessors(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","tasks":[
		{"id":"T1","prompt":"work","max_attempts":2},
		{"id":"T2","prompt":"after","deps":["T1"]}
	]}`)
	disp := newFakeDispatcher()
	disp.reviewFailOnce["tg:T1:r1"] = true
	disp.reviewFailOnce["tg:T1:r2"] = true
	e := newTestEngine(t, g, store, disp, &fakeGate{})

	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if g.Status != StatusFailed {
		t.Fatalf("graph status: %s", g.Status)
	}
	if got := g.TaskByID("T1").Status; got != StatusFailed {
		t.Fatalf("T1: %s", got)
	}
	if got := g.TaskByID("T2").Status; got != StatusBlocked {
		t.Fatalf("T2 must be blocked, got %s", got)
	}
}

func TestEngineExecutorErrorCountsAsAttempt(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","tasks":[{"id":"T1","prompt":"work"}]}`)
	disp := newFakeDispatcher()
	disp.failExecutorOnce["tg:T1:e1"] = true
	e := newTestEngine(t, g, store, disp, &fakeGate{})

	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task := g.TaskByID("T1")
	if task.Status != StatusDone || len(task.Attempts) != 2 {
		t.Fatalf("executor error must consume one attempt: status=%s attempts=%d", task.Status, len(task.Attempts))
	}
}

func TestEngineReviewWaiver(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","require_review":false,"tasks":[{"id":"T1","prompt":"work"}]}`)
	disp := newFakeDispatcher()
	e := newTestEngine(t, g, store, disp, &fakeGate{})
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if g.TaskByID("T1").Status != StatusDone {
		t.Fatalf("status: %s", g.TaskByID("T1").Status)
	}
	for _, id := range disp.callOrder() {
		if strings.Contains(id, ":r") {
			t.Fatalf("no reviewer may be dispatched when review is waived: %v", disp.callOrder())
		}
	}
}

func TestEngineResumeKeepsDoneTasks(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","tasks":[
		{"id":"T1","prompt":"first"},
		{"id":"T2","prompt":"second","deps":["T1"]}
	]}`)
	disp := newFakeDispatcher()
	disp.reviewFailOnce["tg:T2:r1"] = true
	disp.reviewFailOnce["tg:T2:r2"] = true
	disp.reviewFailOnce["tg:T2:r3"] = true
	e := newTestEngine(t, g, store, disp, &fakeGate{})
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if g.TaskByID("T2").Status != StatusFailed {
		t.Fatalf("setup: T2 should have failed, got %s", g.TaskByID("T2").Status)
	}

	// Reload from disk, re-open T2 and resume: T1 must not re-execute.
	loaded, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	t2 := loaded.TaskByID("T2")
	t2.Status = StatusPending
	t2.MaxAttempts = len(t2.Attempts) + 1
	disp2 := newFakeDispatcher()
	e2 := newTestEngine(t, loaded, store, disp2, &fakeGate{})
	if _, err := e2.Run(context.Background()); err != nil {
		t.Fatalf("resume Run: %v", err)
	}
	if loaded.Status != StatusDone {
		t.Fatalf("resumed graph: %s", loaded.Status)
	}
	for _, id := range disp2.callOrder() {
		if strings.HasPrefix(id, "tg:T1:") {
			t.Fatalf("done task re-executed on resume: %v", disp2.callOrder())
		}
	}
}

func TestEngineCancelStopsScheduling(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","tasks":[{"id":"T1","prompt":"work"}]}`)
	disp := newFakeDispatcher()
	e := newTestEngine(t, g, store, disp, &fakeGate{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Run(ctx); err == nil {
		t.Fatal("canceled run must return an error")
	}
	if g.Status != StatusFailed {
		t.Fatalf("canceled graph status: %s", g.Status)
	}
}

func TestRecordCallUsageAttributesOnlyKnownCalls(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","tasks":[{"id":"T1","prompt":"work"}]}`)
	e, err := NewEngine(g, store, Config{
		Dispatcher: newFakeDispatcher(),
		CostPerCall: func(_, _ string, u *models.UsageInfo) float64 {
			return float64(u.TotalTokens) / 1000
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	g.Tasks[0].Attempts = append(g.Tasks[0].Attempts, Attempt{N: 1})
	e.trackCall("tg:T1:e1", "T1")
	e.RecordCallUsage("tg:T1:e1", "openai", "gpt", &models.UsageInfo{TotalTokens: 500})
	e.RecordCallUsage("call-77", "openai", "gpt", &models.UsageInfo{TotalTokens: 9000}) // foreign wave
	if got := g.Tasks[0].CostUSD; got != 0.5 {
		t.Fatalf("cost attribution: %v", got)
	}
	if got := g.Tasks[0].Attempts[0].CostUSD; got != 0.5 {
		t.Fatalf("attempt cost attribution: %v", got)
	}
}

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		in      string
		verdict string
	}{
		{"blah\nVERDICT: PASS — tests green", verdictPass},
		{"blah\nverdict: fail - missing coverage", verdictFail},
		{"**VERDICT:** PASS, all good", verdictPass},
		{"I think it works", verdictFail},
		{"VERDICT: PASS\nVERDICT: FAIL — later line wins", verdictFail},
	}
	for _, c := range cases {
		v, ev := parseVerdict(c.in)
		if v != c.verdict {
			t.Fatalf("parseVerdict(%q) = %q, want %q", c.in, v, c.verdict)
		}
		if ev == "" {
			t.Fatalf("parseVerdict(%q): empty evidence", c.in)
		}
	}
}

// cancelingDispatcher cancels the engine from inside the first dispatch, so
// the run must observe the signal and finish failed.
type cancelingDispatcher struct {
	engine *Engine
}

func (c *cancelingDispatcher) Dispatch(_ context.Context, calls []workers.AgentCall) []workers.AgentResult {
	c.engine.Cancel()
	out := make([]workers.AgentResult, 0, len(calls))
	for _, call := range calls {
		out = append(out, workers.AgentResult{CallID: call.ID, Agent: call.Agent, Error: context.Canceled})
	}
	return out
}

func TestEngineCancelMidRun(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","tasks":[{"id":"T1","prompt":"work"},{"id":"T2","prompt":"after","deps":["T1"]}]}`)
	disp := &cancelingDispatcher{}
	e := newTestEngine(t, g, store, disp, &fakeGate{})
	disp.engine = e
	if _, err := e.Run(context.Background()); err == nil {
		t.Fatal("mid-run cancel must surface as an error")
	}
	if g.Status != StatusFailed {
		t.Fatalf("graph after cancel: %s", g.Status)
	}
}

func TestEngineHeartbeatStreamsChildProgress(t *testing.T) {
	old := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	defer func() { heartbeatInterval = old }()

	g, store := testGraph(t, `{"name":"x","tasks":[{"id":"T1","prompt":"work"}]}`)
	var mu sync.Mutex
	var progress []string
	e, err := NewEngine(g, store, Config{
		Dispatcher: newFakeDispatcher(),
		OnEvent: func(ev Event) {
			if ev.Type == EventProgress {
				mu.Lock()
				progress = append(progress, ev.Detail)
				mu.Unlock()
			}
		},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	parentCtx, parent := runs.Default().Begin(context.Background(), runs.Info{Kind: runs.KindTaskGraph, Agent: "taskgraph", Task: "hb"})
	_, child := runs.Default().Begin(parentCtx, runs.Info{Kind: runs.KindWorker, Agent: "coder", Task: "t"})
	child.SetTurn(3, 30)
	child.SetAction("patch main.go")

	stop := e.startHeartbeat(parentCtx, parent.ID())
	time.Sleep(120 * time.Millisecond)
	stop()
	child.End(nil)
	parent.End(nil)

	mu.Lock()
	defer mu.Unlock()
	if len(progress) == 0 {
		t.Fatal("heartbeat must stream progress while a child is live")
	}
	if !strings.Contains(progress[0], "[coder]") || !strings.Contains(progress[0], "turn 3/30") || !strings.Contains(progress[0], "patch main.go") {
		t.Fatalf("heartbeat detail: %q", progress[0])
	}
}

func TestHeartbeatLabel(t *testing.T) {
	cases := []struct{ callID, agent, want string }{
		{"tg:T3:e2", "coder", "T3 exec·coder"},
		{"tg:T3:r1", "reviewer", "T3 review·reviewer"},
		{"call-7", "coder", "coder"},
		{"", "tester", "tester"},
	}
	for _, c := range cases {
		if got := heartbeatLabel(c.callID, c.agent); got != c.want {
			t.Fatalf("heartbeatLabel(%q,%q) = %q, want %q", c.callID, c.agent, got, c.want)
		}
	}
}

func TestEngineGrantsToolsToExecutorNotReviewer(t *testing.T) {
	g, store := testGraph(t, `{"name":"x","tasks":[{"id":"T1","prompt":"work","tools":["@browser"]}]}`)
	disp := newFakeDispatcher()
	e := newTestEngine(t, g, store, disp, &fakeGate{})
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := disp.grants["tg:T1:e1"]; len(got) != 1 || got[0] != "@browser" {
		t.Fatalf("executor must receive the task grant: %v", got)
	}
	if _, ok := disp.grants["tg:T1:r1"]; ok {
		t.Fatal("reviewer must NEVER receive a plugin grant")
	}
}
