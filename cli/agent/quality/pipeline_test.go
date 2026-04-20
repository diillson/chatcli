/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/diillson/chatcli/cli/agent/workers"
)

// fakeAgent is a minimal WorkerAgent we can drive from tests without
// touching an LLM. It records the task it received in lastTask and returns
// the canned output/error.
type fakeAgent struct {
	output   string
	err      error
	lastTask string
	calls    int
}

func (f *fakeAgent) Type() workers.AgentType   { return workers.AgentType("fake") }
func (f *fakeAgent) Name() string              { return "FakeAgent" }
func (f *fakeAgent) Description() string       { return "fake" }
func (f *fakeAgent) SystemPrompt() string      { return "" }
func (f *fakeAgent) Skills() *workers.SkillSet { return nil }
func (f *fakeAgent) AllowedCommands() []string { return nil }
func (f *fakeAgent) IsReadOnly() bool          { return true }
func (f *fakeAgent) Model() string             { return "" }
func (f *fakeAgent) Effort() string            { return "" }

func (f *fakeAgent) Execute(_ context.Context, task string, _ *workers.WorkerDeps) (*workers.AgentResult, error) {
	f.calls++
	f.lastTask = task
	if f.err != nil {
		return &workers.AgentResult{Output: f.output, Error: f.err}, f.err
	}
	return &workers.AgentResult{Output: f.output}, nil
}

// taskRewriter is a PreHook that prepends a fixed prefix to the task.
type taskRewriter struct {
	name   string
	prefix string
}

func (t *taskRewriter) Name() string { return t.name }
func (t *taskRewriter) PreRun(_ context.Context, hc *HookContext) (string, error) {
	return t.prefix + hc.Task, nil
}

// failingPre always errors. Pipeline must keep going.
type failingPre struct{}

func (failingPre) Name() string { return "failing-pre" }
func (failingPre) PreRun(_ context.Context, _ *HookContext) (string, error) {
	return "", errors.New("boom")
}

// outputRewriter is a PostHook that overwrites Output.
type outputRewriter struct {
	name      string
	newOutput string
}

func (o *outputRewriter) Name() string { return o.name }
func (o *outputRewriter) PostRun(_ context.Context, _ *HookContext, r *workers.AgentResult) error {
	r.Output = o.newOutput
	return nil
}

// failingPost always errors. Pipeline must keep going.
type failingPost struct{}

func (failingPost) Name() string { return "failing-post" }
func (failingPost) PostRun(_ context.Context, _ *HookContext, _ *workers.AgentResult) error {
	return errors.New("boom")
}

func TestPipeline_DisabledShortCircuits(t *testing.T) {
	cfg := Defaults()
	cfg.Enabled = false

	pre := &taskRewriter{name: "rewrite", prefix: "X:"}
	post := &outputRewriter{name: "rewrite-out", newOutput: "REWRITTEN"}

	p := New(cfg, nil).AddPre(pre).AddPost(post)
	a := &fakeAgent{output: "ORIGINAL"}

	res, err := p.Run(context.Background(), a, "task1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.lastTask != "task1" {
		t.Fatalf("disabled pipeline should not invoke pre-hook: got %q", a.lastTask)
	}
	if res.Output != "ORIGINAL" {
		t.Fatalf("disabled pipeline should not invoke post-hook: got %q", res.Output)
	}
}

func TestPipeline_EmptyHooksMatchesDirectExecute(t *testing.T) {
	p := New(Defaults(), nil)
	a := &fakeAgent{output: "OK"}

	res, err := p.Run(context.Background(), a, "task", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.calls != 1 {
		t.Fatalf("agent should run exactly once; got %d", a.calls)
	}
	if a.lastTask != "task" {
		t.Fatalf("task should reach agent unchanged; got %q", a.lastTask)
	}
	if res.Output != "OK" {
		t.Fatalf("output should pass through; got %q", res.Output)
	}
}

func TestPipeline_PreHookRewritesTask(t *testing.T) {
	p := New(Defaults(), nil).AddPre(&taskRewriter{name: "p1", prefix: "[A] "})
	a := &fakeAgent{output: "OK"}

	if _, err := p.Run(context.Background(), a, "do x", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.lastTask != "[A] do x" {
		t.Fatalf("pre-hook rewrite not applied: got %q", a.lastTask)
	}
}

func TestPipeline_PreHooksChainInOrder(t *testing.T) {
	p := New(Defaults(), nil).
		AddPre(&taskRewriter{name: "p1", prefix: "[1]"}).
		AddPre(&taskRewriter{name: "p2", prefix: "[2]"})
	a := &fakeAgent{output: "OK"}

	if _, err := p.Run(context.Background(), a, "x", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// p1 ran first, then p2 saw [1]x and produced [2][1]x
	if a.lastTask != "[2][1]x" {
		t.Fatalf("pre-hooks should chain in order; got %q", a.lastTask)
	}
}

func TestPipeline_PreHookErrorDoesNotBlockExecute(t *testing.T) {
	p := New(Defaults(), nil).AddPre(failingPre{}).AddPre(&taskRewriter{name: "p2", prefix: "X:"})
	a := &fakeAgent{output: "OK"}

	res, err := p.Run(context.Background(), a, "x", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.calls != 1 {
		t.Fatalf("agent should still execute when pre-hook errors; calls=%d", a.calls)
	}
	if a.lastTask != "X:x" {
		t.Fatalf("subsequent pre-hooks must still run after a failure; got %q", a.lastTask)
	}
	if res.Output != "OK" {
		t.Fatalf("output unchanged: got %q", res.Output)
	}
}

func TestPipeline_PostHookMutatesResult(t *testing.T) {
	p := New(Defaults(), nil).AddPost(&outputRewriter{name: "o1", newOutput: "REWRITTEN"})
	a := &fakeAgent{output: "draft"}

	res, err := p.Run(context.Background(), a, "x", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != "REWRITTEN" {
		t.Fatalf("post-hook output rewrite not applied; got %q", res.Output)
	}
}

func TestPipeline_PostHookErrorPreservesResult(t *testing.T) {
	p := New(Defaults(), nil).AddPost(failingPost{})
	a := &fakeAgent{output: "draft"}

	res, err := p.Run(context.Background(), a, "x", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != "draft" {
		t.Fatalf("failing post-hook must not corrupt result; got %q", res.Output)
	}
}

func TestPipeline_AgentExecuteErrorIsReturned(t *testing.T) {
	wantErr := errors.New("agent failure")
	p := New(Defaults(), nil)
	a := &fakeAgent{output: "partial", err: wantErr}

	res, err := p.Run(context.Background(), a, "x", nil)
	if err == nil || err.Error() != wantErr.Error() {
		t.Fatalf("agent error must propagate; got %v", err)
	}
	if res == nil {
		t.Fatalf("result must never be nil")
	}
	if res.Error == nil {
		t.Fatalf("result.Error must carry the agent error")
	}
}

func TestPipeline_NilResultIsReplacedWithEmpty(t *testing.T) {
	// Use a fakeAgent that returns nil; verify pipeline never lets nil
	// reach a PostHook.
	type nilResultAgent struct{ fakeAgent }
	a := &nilResultAgent{}
	a.output = "ignored"

	// Override Execute to return nil result.
	calledPost := false
	post := postHookFn{
		name: "check",
		fn: func(_ context.Context, _ *HookContext, r *workers.AgentResult) error {
			if r == nil {
				return errors.New("nil result reached post-hook")
			}
			calledPost = true
			return nil
		},
	}

	p := New(Defaults(), nil).AddPost(post)
	// Use a custom agent inline that returns (nil, nil).
	res, err := p.Run(context.Background(), &nilReturningAgent{}, "x", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !calledPost {
		t.Fatalf("post-hook should still run; res=%+v", res)
	}
	if res == nil {
		t.Fatalf("pipeline must replace nil result with empty AgentResult")
	}
}

func TestAppliesToAgent(t *testing.T) {
	cases := []struct {
		agent    string
		excludes []string
		want     bool
	}{
		{"refiner", nil, true},
		{"refiner", []string{}, true},
		{"refiner", []string{"formatter"}, true},
		{"refiner", []string{"formatter", "refiner"}, false},
		{"REFINER", []string{"refiner"}, false},   // case-insensitive
		{"refiner", []string{" refiner "}, false}, // trim
	}
	for i, c := range cases {
		if got := AppliesToAgent(c.agent, c.excludes); got != c.want {
			t.Errorf("case %d: AppliesToAgent(%q, %v) = %v, want %v",
				i, c.agent, c.excludes, got, c.want)
		}
	}
}

func TestLoadFromEnv_AppliesOverrides(t *testing.T) {
	t.Setenv("CHATCLI_QUALITY_REFINE_ENABLED", "true")
	t.Setenv("CHATCLI_QUALITY_REFINE_MAX_PASSES", "3")
	t.Setenv("CHATCLI_QUALITY_VERIFY_NUM_QUESTIONS", "5")
	t.Setenv("CHATCLI_QUALITY_PLAN_FIRST_MODE", "always")
	t.Setenv("CHATCLI_QUALITY_REASONING_MODE", "off")
	t.Setenv("CHATCLI_QUALITY_REFINE_EXCLUDE", "formatter, deps , shell")

	cfg := LoadFromEnv()
	if !cfg.Refine.Enabled {
		t.Errorf("Refine.Enabled override not applied")
	}
	if cfg.Refine.MaxPasses != 3 {
		t.Errorf("Refine.MaxPasses=%d, want 3", cfg.Refine.MaxPasses)
	}
	if cfg.Verify.NumQuestions != 5 {
		t.Errorf("Verify.NumQuestions=%d, want 5", cfg.Verify.NumQuestions)
	}
	if cfg.PlanFirst.Mode != "always" {
		t.Errorf("PlanFirst.Mode=%q, want always", cfg.PlanFirst.Mode)
	}
	if cfg.Reasoning.Mode != "off" {
		t.Errorf("Reasoning.Mode=%q, want off", cfg.Reasoning.Mode)
	}
	if got := fmt.Sprint(cfg.Refine.ExcludeAgents); got != "[formatter deps shell]" {
		t.Errorf("Refine.ExcludeAgents=%v", cfg.Refine.ExcludeAgents)
	}
}

func TestLoadFromEnv_InvalidEnumFallsBack(t *testing.T) {
	t.Setenv("CHATCLI_QUALITY_PLAN_FIRST_MODE", "garbage")
	cfg := LoadFromEnv()
	if cfg.PlanFirst.Mode != "auto" {
		t.Errorf("invalid mode should fall back to default; got %q", cfg.PlanFirst.Mode)
	}
}

// ─── test fixtures ────────────────────────────────────────────────────────

// nilReturningAgent's Execute returns (nil, nil) to exercise the
// pipeline's nil-result safety.
type nilReturningAgent struct{}

func (nilReturningAgent) Type() workers.AgentType   { return workers.AgentType("nilret") }
func (nilReturningAgent) Name() string              { return "NilReturning" }
func (nilReturningAgent) Description() string       { return "" }
func (nilReturningAgent) SystemPrompt() string      { return "" }
func (nilReturningAgent) Skills() *workers.SkillSet { return nil }
func (nilReturningAgent) AllowedCommands() []string { return nil }
func (nilReturningAgent) IsReadOnly() bool          { return true }
func (nilReturningAgent) Model() string             { return "" }
func (nilReturningAgent) Effort() string            { return "" }
func (nilReturningAgent) Execute(_ context.Context, _ string, _ *workers.WorkerDeps) (*workers.AgentResult, error) {
	return nil, nil
}

type postHookFn struct {
	name string
	fn   func(context.Context, *HookContext, *workers.AgentResult) error
}

func (p postHookFn) Name() string { return p.name }
func (p postHookFn) PostRun(ctx context.Context, hc *HookContext, r *workers.AgentResult) error {
	return p.fn(ctx, hc, r)
}
