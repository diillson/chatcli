/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import (
	"context"

	"github.com/diillson/chatcli/cli/agent/workers"
)

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
