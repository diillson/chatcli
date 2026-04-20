/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import (
	"context"

	"github.com/diillson/chatcli/cli/agent/workers"
	"go.uber.org/zap"
)

// Pipeline is the middleware that wraps a worker.Execute call with
// pre and post hooks. It implements workers.ExecutionPipeline so the
// dispatcher can swap it in transparently.
//
// Pipeline is safe for concurrent use: AddPre/AddPost mutate hook
// slices and must be called during setup, before the dispatcher starts
// dispatching work. Run itself never modifies the slices.
type Pipeline struct {
	cfg    Config
	pre    []PreHook
	post   []PostHook
	logger *zap.Logger
}

// New creates a Pipeline with the given config. nil logger is upgraded
// to a no-op logger so the caller never has to nil-check.
func New(cfg Config, logger *zap.Logger) *Pipeline {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Pipeline{cfg: cfg, logger: logger}
}

// AddPre registers a PreHook. Hooks fire in registration order.
func (p *Pipeline) AddPre(h PreHook) *Pipeline {
	p.pre = append(p.pre, h)
	return p
}

// AddPost registers a PostHook. Hooks fire in registration order.
func (p *Pipeline) AddPost(h PostHook) *Pipeline {
	p.post = append(p.post, h)
	return p
}

// Config returns the live config snapshot. Returned by value — pipeline
// configuration is immutable after construction.
func (p *Pipeline) Config() Config {
	return p.cfg
}

// HookCounts reports how many pre and post hooks are registered. Used by
// /config quality so the user can see at a glance whether hooks are
// actually wired (a non-zero count means a phase delivered its hook).
func (p *Pipeline) HookCounts() (pre, post int) {
	return len(p.pre), len(p.post)
}

// Run wraps agent.Execute with the registered hooks.
//
// Order:
//  1. PreHooks (each may rewrite task; errors logged and skipped)
//  2. agent.Execute(rewrittenTask)
//  3. PostHooks (each may mutate result; errors logged and skipped)
//
// When the pipeline master switch is off (cfg.Enabled == false), the
// pipeline degenerates to a direct agent.Execute call — preserving the
// "no-pipeline" performance contract for users who explicitly disable it.
func (p *Pipeline) Run(
	ctx context.Context,
	agent workers.WorkerAgent,
	task string,
	deps *workers.WorkerDeps,
) (*workers.AgentResult, error) {
	if !p.cfg.Enabled {
		return agent.Execute(ctx, task, deps)
	}

	// Phase 1 (#7): cross-provider reasoning auto-enable. Attaches an
	// effort hint to ctx for agents in cfg.Reasoning.AutoAgents when no
	// hint is already present. The effort tier maps to Anthropic
	// thinking_budget and OpenAI reasoning_effort via skill_hints.
	ctx = applyAutoReasoning(ctx, p.cfg.Reasoning, agent)

	hc := &HookContext{
		Agent:  agent,
		Task:   task,
		Deps:   deps,
		Config: p.cfg,
	}

	currentTask := task
	for _, h := range p.pre {
		newTask, err := h.PreRun(ctx, hc)
		if err != nil {
			p.logger.Warn("quality pre-hook failed; continuing",
				zap.String("hook", h.Name()),
				zap.String("agent", string(agent.Type())),
				zap.Error(err))
			continue
		}
		if newTask != "" {
			currentTask = newTask
			hc.Task = newTask
		}
	}

	result, execErr := agent.Execute(ctx, currentTask, deps)
	if result == nil {
		result = &workers.AgentResult{}
	}

	for _, h := range p.post {
		if err := h.PostRun(ctx, hc, result); err != nil {
			p.logger.Warn("quality post-hook failed; continuing",
				zap.String("hook", h.Name()),
				zap.String("agent", string(agent.Type())),
				zap.Error(err))
		}
	}

	return result, execErr
}

// Compile-time assertion: Pipeline satisfies workers.ExecutionPipeline.
var _ workers.ExecutionPipeline = (*Pipeline)(nil)
