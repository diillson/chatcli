/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeSuite(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaultsAndValidates(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "fx"), 0o750); err != nil {
		t.Fatal(err)
	}
	p := writeSuite(t, dir, "core.yaml", `
name: core
defaults:
  mode: chat
  trials: 2
  env: {A: "1"}
  checks:
    - not_contains: "As an AI"
cases:
  - id: one
    prompt: hi
    env: {B: "2"}
    checks:
      - contains: hello
  - id: two
    mode: coder
    fixture: fx
    trials: 3
    pass_policy: any
    prompt: fix it
    checks:
      - judge: {rubric: "is correct"}
`)
	suites, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	s := suites[0]
	one, two := s.Cases[0], s.Cases[1]
	if one.Mode != ModeChat || one.Trials != 2 || one.PassPolicy != PassAll || one.Timeout.D() != DefaultTimeout {
		t.Errorf("defaults not applied: %+v", one)
	}
	if one.Env["A"] != "1" || one.Env["B"] != "2" {
		t.Errorf("env not merged: %v", one.Env)
	}
	if len(one.Checks) != 2 || one.Checks[1].Kind() != KindNotContains {
		t.Errorf("default checks not appended: %+v", one.Checks)
	}
	if two.Trials != 3 || two.PassPolicy != PassAny || two.FixtureDir != filepath.Join(dir, "fx") {
		t.Errorf("case overrides lost: %+v", two)
	}
	j := two.Checks[0].Judge
	if j.Threshold != DefaultThreshold || j.Samples != 1 {
		t.Errorf("judge defaults not applied: %+v", j)
	}
}

func TestLoadRejectsBadSuites(t *testing.T) {
	cases := map[string]string{
		"unknown key":     "cases:\n  - id: a\n    prompt: x\n    chekcs: []\n",
		"no checks":       "cases:\n  - id: a\n    prompt: x\n",
		"two kinds":       "cases:\n  - id: a\n    prompt: x\n    checks:\n      - {contains: a, regex: b}\n",
		"bad regex":       "cases:\n  - id: a\n    prompt: x\n    checks:\n      - regex: '('\n",
		"dup id":          "cases:\n  - {id: a, prompt: x, checks: [{contains: a}]}\n  - {id: a, prompt: x, checks: [{contains: a}]}\n",
		"bad id":          "cases:\n  - {id: 'a b', prompt: x, checks: [{contains: a}]}\n",
		"escape path":     "cases:\n  - {id: a, prompt: x, checks: [{file_exists: ../etc/passwd}]}\n",
		"absolute path":   "cases:\n  - {id: a, prompt: x, checks: [{file_exists: /etc/passwd}]}\n",
		"escaping file":   "cases:\n  - {id: a, prompt: x, files: {'../x': y}, checks: [{contains: a}]}\n",
		"bad mode":        "cases:\n  - {id: a, mode: agent, prompt: x, checks: [{contains: a}]}\n",
		"no prompt":       "cases:\n  - {id: a, checks: [{contains: a}]}\n",
		"too many trials": "cases:\n  - {id: a, trials: 99, prompt: x, checks: [{contains: a}]}\n",
		"threshold":       "cases:\n  - {id: a, prompt: x, checks: [{judge: {rubric: r, threshold: 2}}]}\n",
		"missing fixture": "cases:\n  - {id: a, fixture: nope, prompt: x, checks: [{contains: a}]}\n",
		"policy action":   "cases:\n  - {id: a, prompt: x, policy: [{pattern: '@coder', action: yolo}], checks: [{contains: a}]}\n",
		"no cases":        "name: empty\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p := writeSuite(t, t.TempDir(), "s.yaml", body)
			if _, err := Load(p); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func TestLoadDirectoryIsNotRecursive(t *testing.T) {
	dir := t.TempDir()
	writeSuite(t, dir, "a.yaml", "cases:\n  - {id: a, prompt: x, checks: [{contains: a}]}\n")
	writeSuite(t, dir, "b.yml", "cases:\n  - {id: b, prompt: x, checks: [{contains: b}]}\n")
	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeSuite(t, filepath.Join(dir, "fixtures"), "k8s.yaml", "kind: Deployment\n")
	suites, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(suites) != 2 || suites[0].Name != "a" || suites[1].Name != "b" {
		t.Fatalf("got %d suites", len(suites))
	}
}

func TestFilter(t *testing.T) {
	s := &Suite{Name: "s", Cases: []*Case{
		{ID: "chat-pt", Tags: []string{"i18n"}},
		{ID: "coder-fix", Tags: []string{"go"}},
		{ID: "coder-new"},
	}}
	ids := func(sel ...string) []string {
		var out []string
		for _, s := range Filter([]*Suite{s}, sel) {
			for _, c := range s.Cases {
				out = append(out, c.ID)
			}
		}
		return out
	}
	if got := ids("coder-*"); strings.Join(got, ",") != "coder-fix,coder-new" {
		t.Errorf("glob: %v", got)
	}
	if got := ids("tag:I18N", "coder-new"); strings.Join(got, ",") != "chat-pt,coder-new" {
		t.Errorf("tag+id: %v", got)
	}
	if got := ids(""); len(got) != 3 {
		t.Errorf("empty selector keeps all: %v", got)
	}
	if got := ids("nope"); len(got) != 0 {
		t.Errorf("no match: %v", got)
	}
}

func TestSandboxPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sb")
	ok := []string{"a.go", "dir/b.go", "./c", "dir/../d"}
	for _, p := range ok {
		got, err := SandboxPath(root, p)
		if err != nil || !strings.HasPrefix(got, root) {
			t.Errorf("%q: %v %q", p, err, got)
		}
	}
	bad := []string{"", "../x", "a/../../x", "/etc/passwd", `\x`}
	for _, p := range bad {
		if _, err := SandboxPath(root, p); err == nil {
			t.Errorf("%q must be refused", p)
		}
	}
}

func f64(v float64) *float64 { return &v }
func intp(v int) *int        { return &v }

func TestDeterministicChecks(t *testing.T) {
	sb := t.TempDir()
	if err := os.WriteFile(filepath.Join(sb, "main.go"), []byte("package main\nfunc Sum() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := &Outcome{
		Final:    "Here:\n```json\n{\"name\": \"x\", \"n\": 1}\n```\nThe capital is Brasília.",
		Duration: 2 * time.Second,
		Sandbox:  sb,
		Record: &Record{
			Turns: 3, CostUSD: 0.02,
			ToolCalls: []ToolCall{{Name: "@coder", Args: `{"cmd":"exec","args":{"cmd":"go test"}}`}, {Name: "@webfetch", Args: "--url x"}},
		},
	}
	tests := []struct {
		name string
		c    Check
		want bool
	}{
		{"contains", Check{Contains: "Brasília"}, true},
		{"contains case", Check{Contains: "brasília", IgnoreCase: true}, true},
		{"contains miss", Check{Contains: "brasília"}, false},
		{"not_contains", Check{NotContains: "As an AI"}, true},
		{"regex", Check{Regex: `capital is \w+`}, true},
		{"not_regex hit", Check{NotRegex: `(?i)brasília`}, false},
		{"equals", Check{Equals: "nope"}, false},
		{"json keys", Check{JSON: &JSONCheck{RequireKeys: []string{"name", "n"}}}, true},
		{"json missing key", Check{JSON: &JSONCheck{RequireKeys: []string{"zzz"}}}, false},
		{"file exists", Check{FileExists: "main.go"}, true},
		{"file absent", Check{FileAbsent: "main.go"}, false},
		{"file contains", Check{FileContains: &FileCheck{Path: "main.go", Text: "func Sum"}}, true},
		{"file regex", Check{FileRegex: &FileCheck{Path: "main.go", Text: `^package \w+`}}, true},
		{"file missing", Check{FileContains: &FileCheck{Path: "nope.go", Text: "x"}}, false},
		{"tool called", Check{ToolCalled: "@coder"}, true},
		{"tool subcommand json", Check{ToolCalled: "@coder exec"}, true},
		{"tool subcommand argv", Check{ToolCalled: "@webfetch --url"}, true},
		{"tool wrong sub", Check{ToolCalled: "@coder write"}, false},
		{"tool not called", Check{ToolNotCalled: "@browser"}, true},
		{"cost ok", Check{MaxCostUSD: f64(0.05)}, true},
		{"cost over", Check{MaxCostUSD: f64(0.01)}, false},
		{"turns", Check{MaxTurns: intp(2)}, false},
		{"duration", Check{MaxDuration: Duration(3 * time.Second)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.c.validate(); err != nil {
				t.Fatalf("invalid check: %v", err)
			}
			res := evaluate(context.Background(), &tt.c, out, nil)
			if res.Passed != tt.want {
				t.Fatalf("passed=%v want %v (%s)", res.Passed, tt.want, res.Detail)
			}
			if res.Passed && (res.Score != 1 || res.Detail != "") {
				t.Errorf("a pass scores 1 with no detail: %+v", res)
			}
			if !res.Passed && res.Detail == "" {
				t.Errorf("a failure must explain itself")
			}
		})
	}
}

func TestCommandCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh-based fixture")
	}
	sb := t.TempDir()
	out := &Outcome{Sandbox: sb, Env: map[string]string{"EVAL_X": "42"}}
	ok := Check{Command: &CommandCheck{Run: `test "$EVAL_X" = 42 && echo fine`, Contains: "fine"}}
	if r := evaluate(context.Background(), &ok, out, nil); !r.Passed {
		t.Fatalf("expected pass: %s", r.Detail)
	}
	bad := Check{Command: &CommandCheck{Run: "exit 3"}}
	if r := evaluate(context.Background(), &bad, out, nil); r.Passed || !strings.Contains(r.Detail, "3") {
		t.Fatalf("expected exit failure: %+v", r)
	}
	expect := Check{Command: &CommandCheck{Run: "exit 3", ExpectExit: 3}}
	if r := evaluate(context.Background(), &expect, out, nil); !r.Passed {
		t.Fatalf("expect_exit 3 should pass: %s", r.Detail)
	}
	slow := Check{Command: &CommandCheck{Run: "sleep 5", Timeout: Duration(100 * time.Millisecond)}}
	if r := evaluate(context.Background(), &slow, out, nil); r.Passed {
		t.Fatal("timeout must fail")
	}
}

func TestExtractJSON(t *testing.T) {
	for in, want := range map[string]bool{
		`{"a":1}`:                     true,
		"text ```json\n[1,2]\n``` x":  true,
		`answer: {"a": {"b": 2}} end`: true,
		"no json here":                false,
		"":                            false,
	} {
		if _, ok := extractJSON(in); ok != want {
			t.Errorf("%q: got %v", in, ok)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	tests := map[string]float64{
		`{"score": 0.8, "reasoning": "ok"}`:                 0.8,
		"```json\n{\"score\": 1, \"reasoning\": \"\"}\n```": 1,
		`Sure! {"score": 7, "reasoning": "decent"}`:         0.7,
		`{"score": 85}`: 0.85,
	}
	for in, want := range tests {
		v, err := ParseVerdict(in)
		if err != nil || v.Score < want-1e-9 || v.Score > want+1e-9 {
			t.Errorf("%q: %v %v", in, v.Score, err)
		}
	}
	for _, in := range []string{"great answer!", `{"score": -1}`, `{"score": 500}`, `{"reasoning": "x"}`} {
		if _, err := ParseVerdict(in); err == nil {
			t.Errorf("%q should not parse", in)
		}
	}
}

func TestBuildJudgePromptHidesModelAndCaps(t *testing.T) {
	p := BuildJudgePrompt(JudgeRequest{
		Task: "t", Rubric: "r", Reference: "ref",
		Answer:    strings.Repeat("x", maxJudgeAnswer+10),
		ToolCalls: []ToolCall{{Name: "@coder", Args: "read"}},
	})
	for _, want := range []string{"### RUBRIC\nr", "### REFERENCE ANSWER\nref", "- @coder read", "[truncated]"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

type fakeJudge struct {
	scores []float64
	err    error
	calls  atomic.Int32
}

func (f *fakeJudge) Grade(_ context.Context, _ JudgeRequest) (JudgeVerdict, error) {
	i := int(f.calls.Add(1)) - 1
	if f.err != nil {
		return JudgeVerdict{CostUSD: 0.001}, f.err
	}
	return JudgeVerdict{Score: f.scores[i%len(f.scores)], Reasoning: "r", CostUSD: 0.001}, nil
}

func TestJudgeCheckMedianAndFailures(t *testing.T) {
	c := Check{Judge: &JudgeCheck{Rubric: "r", Threshold: 0.7, Samples: 3}}
	out := &Outcome{Final: "a"}
	j := &fakeJudge{scores: []float64{0.2, 0.9, 0.8}}
	r := evaluate(context.Background(), &c, out, j)
	if !r.Passed || r.Score != 0.8 || j.calls.Load() != 3 {
		t.Fatalf("median of 0.2/0.9/0.8 is 0.8: %+v", r)
	}
	if r.CostUSD < 0.003-1e-9 {
		t.Errorf("judge cost not accumulated: %v", r.CostUSD)
	}
	failing := &fakeJudge{err: errors.New("boom")}
	if r := evaluate(context.Background(), &c, out, failing); r.Passed {
		t.Fatal("a failed judge call must never pass")
	}
	if r := evaluate(context.Background(), &c, out, nil); r.Passed {
		t.Fatal("no judge must never pass")
	}
}

// fakeExec answers per case id from a script of outcomes, one per call.
type fakeExec struct {
	mu      sync.Mutex
	script  map[string][]fakeRun
	calls   map[string]int
	workdir map[string]string
}

type fakeRun struct {
	final string
	cost  float64
	err   error
	write map[string]string // files the "candidate" writes into the sandbox
}

func (f *fakeExec) Execute(_ context.Context, req Request) (*Execution, error) {
	f.mu.Lock()
	id := strings.SplitN(req.Prompt, ":", 2)[0]
	n := f.calls[id]
	f.calls[id] = n + 1
	if f.workdir != nil {
		f.workdir[id] = req.Workdir
	}
	runs := f.script[id]
	r := runs[n%len(runs)]
	f.mu.Unlock()
	for name, body := range r.write {
		if err := os.WriteFile(filepath.Join(req.Workdir, name), []byte(body), 0o600); err != nil {
			return nil, err
		}
	}
	ex := &Execution{Final: r.final, Duration: 10 * time.Millisecond, Record: &Record{Final: r.final, CostUSD: r.cost, Provider: "P", Model: "m"}}
	return ex, r.err
}

func newFakeExec(script map[string][]fakeRun) *fakeExec {
	return &fakeExec{script: script, calls: map[string]int{}, workdir: map[string]string{}}
}

func mkCase(id, policy string, trials int, checks ...Check) *Case {
	return &Case{ID: id, Mode: ModeChat, Prompt: id + ": go", Trials: trials, PassPolicy: policy, Timeout: Duration(time.Minute), Checks: checks}
}

func TestRunAggregatesTrialsAndPolicies(t *testing.T) {
	ok := fakeRun{final: "yes"}
	no := fakeRun{final: "no"}
	s := &Suite{Name: "s", Cases: []*Case{
		mkCase("stable", PassAll, 3, Check{Contains: "yes"}),
		mkCase("flaky-all", PassAll, 3, Check{Contains: "yes"}),
		mkCase("flaky-any", PassAny, 3, Check{Contains: "yes"}),
		mkCase("flaky-maj", PassMajority, 3, Check{Contains: "yes"}),
		mkCase("broken", PassAll, 2, Check{Contains: "yes"}),
		{ID: "skipped", Skip: "not ready", Prompt: "x", Checks: []Check{{Contains: "x"}}},
	}}
	ex := newFakeExec(map[string][]fakeRun{
		"stable":    {ok},
		"flaky-all": {ok, no, ok},
		"flaky-any": {no, ok, no},
		"flaky-maj": {ok, no, ok},
		"broken":    {{err: errors.New("crash")}},
	})
	var events atomic.Int32
	rep := Run(context.Background(), []*Suite{s}, Options{Executor: ex, Concurrency: 1, Progress: func(TrialEvent) { events.Add(1) }})
	got := map[string]CaseResult{}
	for _, c := range rep.Cases {
		got[c.ID] = c
	}
	want := map[string]string{
		"stable": StatusPass, "flaky-all": StatusFail, "flaky-any": StatusPass,
		"flaky-maj": StatusPass, "broken": StatusError, "skipped": StatusSkipped,
	}
	for id, st := range want {
		if got[id].Status != st {
			t.Errorf("%s: status %s want %s", id, got[id].Status, st)
		}
	}
	if !got["flaky-all"].Flaky || !got["flaky-all"].PassAtK || got["flaky-all"].PassHatK {
		t.Errorf("flaky-all metrics: %+v", got["flaky-all"])
	}
	if events.Load() != 14 {
		t.Errorf("progress events: %d", events.Load())
	}
	sum := rep.Summary
	if sum.Cases != 6 || sum.Passed != 3 || sum.Failed != 1 || sum.Errored != 1 || sum.Skipped != 1 || sum.Flaky != 3 {
		t.Errorf("summary: %+v", sum)
	}
	if want := 3.0 / 5.0; sum.PassRate < want-1e-9 || sum.PassRate > want+1e-9 {
		t.Errorf("pass rate %v want %v", sum.PassRate, want)
	}
	if rep.Candidate.Provider != "P" || rep.Candidate.Model != "m" {
		t.Errorf("candidate: %+v", rep.Candidate)
	}
}

func TestRunSandboxFixtureFilesPolicyAndCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh-based setup")
	}
	fx := t.TempDir()
	if err := os.WriteFile(filepath.Join(fx, "seed.txt"), []byte("seed"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Case{
		ID: "coder", Mode: ModeCoder, Prompt: "coder: go", Trials: 1, PassPolicy: PassAll,
		Timeout: Duration(time.Minute), FixtureDir: fx,
		Files: map[string]string{"sub/inline.txt": "inline"},
		Setup: []string{"echo from-setup > setup.txt"},
		Checks: []Check{
			{FileContains: &FileCheck{Path: "seed.txt", Text: "seed"}},
			{FileContains: &FileCheck{Path: "sub/inline.txt", Text: "inline"}},
			{FileContains: &FileCheck{Path: "setup.txt", Text: "from-setup"}},
			{FileContains: &FileCheck{Path: "coder_policy.json", Text: `"@coder exec"`}},
			{FileContains: &FileCheck{Path: "out.txt", Text: "written"}},
		},
	}
	ex := newFakeExec(map[string][]fakeRun{"coder": {{final: "done", write: map[string]string{"out.txt": "written"}}}})
	rep := Run(context.Background(), []*Suite{{Name: "s", Cases: []*Case{c}}}, Options{Executor: ex})
	cr := rep.Cases[0]
	if cr.Status != StatusPass {
		t.Fatalf("status %s: %+v", cr.Status, cr.Trials)
	}
	if _, err := os.Stat(ex.workdir["coder"]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sandbox not removed: %v", err)
	}

	rep = Run(context.Background(), []*Suite{{Name: "s", Cases: []*Case{c}}}, Options{Executor: ex, Keep: true})
	kept := rep.Cases[0].Trials[0].Sandbox
	if kept == "" {
		t.Fatal("--keep must report the sandbox")
	}
	defer func() { _ = os.RemoveAll(kept) }()
	if _, err := os.Stat(filepath.Join(kept, "out.txt")); err != nil {
		t.Errorf("kept sandbox lost its files: %v", err)
	}
}

func TestRunSetupFailureIsAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh-based setup")
	}
	c := mkCase("x", PassAll, 1, Check{Contains: "y"})
	c.Setup = []string{"exit 7"}
	ex := newFakeExec(map[string][]fakeRun{"x": {{final: "y"}}})
	rep := Run(context.Background(), []*Suite{{Name: "s", Cases: []*Case{c}}}, Options{Executor: ex})
	if rep.Cases[0].Status != StatusError || ex.calls["x"] != 0 {
		t.Fatalf("setup failure must error before running the candidate: %+v", rep.Cases[0])
	}
}

func TestRunBudgetCapSkipsRemainingTrials(t *testing.T) {
	ex := newFakeExec(map[string][]fakeRun{"a": {{final: "y", cost: 0.6}}})
	c := mkCase("a", PassAll, 5, Check{Contains: "y"})
	rep := Run(context.Background(), []*Suite{{Name: "s", Cases: []*Case{c}}}, Options{Executor: ex, Concurrency: 1, MaxCostUSD: 1})
	if !rep.BudgetExceeded || ex.calls["a"] != 2 {
		t.Fatalf("budget: exceeded=%v calls=%d", rep.BudgetExceeded, ex.calls["a"])
	}
	cr := rep.Cases[0]
	if cr.Status != StatusPass || ranTrials(&cr) != 2 {
		t.Errorf("graded trials only count: %+v", cr)
	}
}

func TestRunCanceledContextSkips(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex := newFakeExec(map[string][]fakeRun{"a": {{final: "y"}}})
	rep := Run(ctx, []*Suite{{Name: "s", Cases: []*Case{mkCase("a", PassAll, 2, Check{Contains: "y"})}}}, Options{Executor: ex})
	if rep.Cases[0].Status != StatusSkipped || ex.calls["a"] != 0 {
		t.Fatalf("canceled run must not execute: %+v", rep.Cases[0])
	}
}

func TestCompareAndGate(t *testing.T) {
	mk := func(statuses map[string]string, _ float64) *Report {
		r := &Report{Schema: ReportSchema}
		for id, st := range statuses {
			r.Cases = append(r.Cases, CaseResult{Suite: "s", ID: id, Status: st})
		}
		return r
	}
	base := mk(map[string]string{"a": StatusPass, "b": StatusFail, "c": StatusPass, "gone": StatusPass, "sk": StatusPass}, 0.75)
	cur := mk(map[string]string{"a": StatusFail, "b": StatusPass, "c": StatusPass, "new": StatusPass, "sk": StatusSkipped}, 0.75)
	cmp := Compare(base, cur)
	if len(cmp.Regressions) != 1 || cmp.Regressions[0].Key != "s/a" {
		t.Errorf("regressions: %+v", cmp.Regressions)
	}
	if len(cmp.Fixes) != 1 || cmp.Fixes[0].Key != "s/b" {
		t.Errorf("fixes: %+v", cmp.Fixes)
	}
	if strings.Join(cmp.Added, ",") != "s/new" || strings.Join(cmp.Removed, ",") != "s/gone" {
		t.Errorf("added/removed: %v %v", cmp.Added, cmp.Removed)
	}
	if !cmp.Regressed(0, 0) || cmp.Regressed(1, 0) {
		t.Error("max-regressions gate")
	}
	// Common graded cases are a, b, c (gone/new are one-sided, sk skipped):
	// 2/3 passed before (a, c), 2/3 after (b, c).
	if cmp.Common != 3 || cmp.PassRateBefore != 2.0/3 || cmp.PassRateAfter != 2.0/3 {
		t.Errorf("rates over common cases: %+v", cmp)
	}
	// A filtered run against a full baseline is not a pass-rate drop.
	full := mk(map[string]string{"a": StatusPass, "b": StatusPass, "c": StatusFail}, 0)
	filtered := mk(map[string]string{"a": StatusPass}, 0)
	if fc := Compare(full, filtered); fc.Regressed(0, 0) || fc.PassRateBefore != 1 {
		t.Errorf("filtered run flagged: %+v", fc)
	}
	drop := &Comparison{PassRateBefore: 0.9, PassRateAfter: 0.8}
	if !drop.Regressed(0, 0.05) || drop.Regressed(0, 0.1) {
		t.Error("tolerance gate")
	}
}

func TestReportRoundTripAndRender(t *testing.T) {
	ex := newFakeExec(map[string][]fakeRun{"a": {{final: "yes"}}, "b": {{final: "no"}}})
	s := &Suite{Name: "s", Cases: []*Case{mkCase("a", PassAll, 1, Check{Contains: "yes"}), mkCase("b", PassAll, 1, Check{Contains: "yes"})}}
	rep := Run(context.Background(), []*Suite{s}, Options{Executor: ex})
	p := filepath.Join(t.TempDir(), "r", "report.json")
	if err := WriteReport(p, rep); err != nil {
		t.Fatal(err)
	}
	back, err := ReadReport(p)
	if err != nil {
		t.Fatal(err)
	}
	if back.Summary.Passed != 1 || len(back.Cases) != 2 {
		t.Fatalf("round trip: %+v", back.Summary)
	}
	if !back.MeetsPassRate(0.5) || back.MeetsPassRate(0.51) {
		t.Error("pass-rate gate")
	}
	var sb strings.Builder
	WriteSummary(&sb, back)
	if !strings.Contains(sb.String(), "s/b") {
		t.Errorf("summary lacks failing case: %s", sb.String())
	}
	md := Markdown(back, Compare(back, back))
	if !strings.Contains(md, "`s/a`") || !strings.Contains(md, "|") {
		t.Errorf("markdown: %s", md)
	}
	if err := os.WriteFile(p, []byte(`{"schema": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(p); err == nil {
		t.Error("future schema must be refused")
	}
}

func TestRecordRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rec.json")
	in := &Record{Mode: ModeCoder, Final: "ok", ToolCalls: []ToolCall{{Name: "@coder"}, {Name: "@coder"}, {Name: "@read"}}}
	if err := WriteRecord(p, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadRecord(p)
	if err != nil {
		t.Fatal(err)
	}
	if out.Schema != RecordSchema || out.Final != "ok" || strings.Join(out.ToolNames(), ",") != "@coder,@read" {
		t.Fatalf("round trip: %+v", out)
	}
	var nilRec *Record
	if nilRec.ToolNames() != nil {
		t.Error("nil record has no tools")
	}
}

func TestBinaryExecutorEnv(t *testing.T) {
	h := (&BinaryExecutor{Dotenv: "/x/.env"}).BaseEnv()
	if h["CHATCLI_MEMORY_ENABLED"] != "false" || h["CHATCLI_SESSION_AUTORECALL"] != "false" || h["CHATCLI_DOTENV"] != "/x/.env" {
		t.Errorf("hermetic env: %v", h)
	}
	m := (&BinaryExecutor{WithMemory: true}).BaseEnv()
	if _, set := m["CHATCLI_MEMORY_ENABLED"]; set {
		t.Error("--with-memory must leave memory as configured")
	}
	if h["CHATCLI_HOOKS_ENABLED"] != "false" || m["CHATCLI_HOOKS_ENABLED"] != "false" {
		t.Error("user hooks are off in every candidate run, with or without memory")
	}
	if m["CHATCLI_SESSION_AUTOSAVE"] != "false" || m["CHATCLI_CODER_CHECKPOINTS"] != "off" {
		t.Errorf("debris switches stay off with memory: %v", m)
	}
}

func TestPercentile(t *testing.T) {
	xs := []int64{5, 1, 4, 2, 3}
	if percentile(xs, 50) != 3 || percentile(xs, 95) != 5 || percentile(nil, 50) != 0 {
		t.Error("nearest-rank percentile")
	}
}

func TestDefaultCoderPolicyCoversEverySubcommand(t *testing.T) {
	rules := defaultCoderPolicy()
	have := map[string]bool{}
	for _, r := range rules {
		if r.Action != "allow" {
			t.Errorf("%s: %s", r.Pattern, r.Action)
		}
		have[r.Pattern] = true
	}
	// The subcommands a user's global policy commonly pins to "ask": each
	// needs its own exact-pattern override or the more specific global rule
	// wins and the eval blocks on a prompt nobody can answer.
	for _, sub := range []string{"@coder", "@coder write", "@coder patch", "@coder multipatch", "@coder exec", "@coder test", "@coder read"} {
		if !have[sub] {
			t.Errorf("default policy lacks %q (have %v)", sub, rules)
		}
	}
}
