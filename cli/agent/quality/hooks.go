/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import (
	"context"
	"errors"

	"github.com/diillson/chatcli/cli/agent/workers"
)

// ErrSkipExecution is a sentinel a PreHook may return to tell the
// Pipeline to skip the underlying agent.Execute call. Subsequent
// PreHooks are NOT run; PostHooks are run as usual against whatever
// the PreHook left on the pipeline context (typically a short-circuit
// output like a cache hit or a fast-path refusal).
//
// When a PreHook short-circuits, the Pipeline synthesizes an
// AgentResult with Output populated from PipelineShortCircuit (on the
// hook context) so PostHooks have something to work with. Hooks that
// want to short-circuit with a specific output must call
// HookContext.SetShortCircuit before returning ErrSkipExecution.
var ErrSkipExecution = errors.New("quality: skip agent execute")

// ErrSkipRemainingHooks is a sentinel a hook (pre or post) may return
// to tell the Pipeline to stop running subsequent hooks of the same
// phase. agent.Execute still runs if this is returned from a PreHook
// (unlike ErrSkipExecution). Useful when a refinement hook detects
// that a later hook would be redundant or unsafe.
var ErrSkipRemainingHooks = errors.New("quality: skip remaining hooks")

// HookContext is the bag of state passed to every hook so it can decide
// whether to run and how. Fields are set by the Pipeline before each hook
// invocation; hooks may read but should not mutate Agent or Deps.
type HookContext struct {
	// Agent is the worker that will (or did) execute the task.
	Agent workers.WorkerAgent
	// Task is the natural-language task description. Pre-hooks can rewrite
	// it via the return value; the rewrite then becomes the input to the
	// agent and is reflected back in HookContext.Task for subsequent hooks.
	Task string
	// Deps is the same WorkerDeps the dispatcher built (LLM client, lock
	// manager, policy checker, logger). Hooks needing to call the LLM
	// should reuse Deps.LLMClient so model routing/effort hints stay
	// consistent with the wrapped worker.
	Deps *workers.WorkerDeps
	// Config is the live quality config snapshot.
	Config Config

	// shortCircuitOutput, when non-empty, is the Output the Pipeline
	// uses for the AgentResult that post-hooks see when a PreHook
	// returns ErrSkipExecution. Unexported so only the PreHook (via
	// SetShortCircuit) can write it.
	shortCircuitOutput string
	shortCircuitSet    bool
}

// SetShortCircuit tells the Pipeline what Output to synthesize when a
// PreHook short-circuits agent execution. Must be called before
// returning ErrSkipExecution — subsequent PostHooks read this as the
// AgentResult.Output. Empty string is permitted (callers can still
// signal "short-circuit with no output") but the zero state (never
// called) falls back to "".
func (hc *HookContext) SetShortCircuit(output string) {
	if hc == nil {
		return
	}
	hc.shortCircuitOutput = output
	hc.shortCircuitSet = true
}

// ShortCircuitOutput returns the short-circuit output a PreHook set.
// Exposed for tests and for the Pipeline itself to honor the value.
func (hc *HookContext) ShortCircuitOutput() (string, bool) {
	if hc == nil {
		return "", false
	}
	return hc.shortCircuitOutput, hc.shortCircuitSet
}

// PreHook runs before the agent executes. It may rewrite the task by
// returning a non-empty string. Returning an error causes the pipeline to
// log a warning and proceed (best effort: a broken pre-hook must never
// block legitimate work).
type PreHook interface {
	// Name identifies the hook for logs and exclude lists.
	Name() string
	// PreRun is invoked once per Pipeline.Run call before the agent.
	// Returning an empty newTask leaves the task unchanged.
	PreRun(ctx context.Context, hc *HookContext) (newTask string, err error)
}

// PostHook runs after the agent executes. It may mutate the result in
// place (e.g. rewrite Output, append Metadata). Returning an error logs
// a warning and proceeds; the original result is preserved.
type PostHook interface {
	// Name identifies the hook for logs and exclude lists.
	Name() string
	// PostRun is invoked once per Pipeline.Run call after the agent.
	// result is never nil.
	PostRun(ctx context.Context, hc *HookContext, result *workers.AgentResult) error
}

// Prioritized is an optional interface a hook may implement to
// influence its execution order within its phase. Lower values run
// first; the default (for hooks that don't implement this) is 100.
// Ties resolve by registration order (stable sort).
//
// Enterprise deployments may run multiple pre/post hooks whose
// correct ordering matters (e.g. a caching hook must run before a
// cost-tracking hook). Registration order alone is brittle when
// plugins contribute hooks from outside the core; Prioritized makes
// the contract explicit.
type Prioritized interface {
	// Priority returns an ordering hint. Suggested scale: 0–1000.
	// Convention: 50 = very-early (cache, short-circuit), 100 =
	// default, 200 = late (audit, metrics).
	Priority() int
}

// DefaultPriority is the priority assigned to hooks that don't
// implement the Prioritized interface.
const DefaultPriority = 100

// priorityOf returns the hook's declared priority, or DefaultPriority
// when the hook doesn't implement Prioritized.
func priorityOf(h any) int {
	if p, ok := h.(Prioritized); ok {
		return p.Priority()
	}
	return DefaultPriority
}
