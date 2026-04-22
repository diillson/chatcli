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
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/cli/agent/workers"
	"go.uber.org/zap"
)

// defaultHookTimeout bounds a single hook's execution when the
// pipeline config doesn't specify one explicitly. 30s is long enough
// for multi-pass LLM refinement but short enough that a wedged hook
// doesn't stall a whole turn.
const defaultHookTimeout = 30 * time.Second

// Pipeline is the middleware that wraps a worker.Execute call with
// pre and post hooks. It implements workers.ExecutionPipeline so the
// dispatcher can swap it in transparently.
//
// Thread safety: Pipeline is safe for concurrent use from any number
// of goroutines. Registrations (AddPre/AddPost) and config updates
// (SwapConfig) go through an atomic snapshot swap, so in-flight Run()
// calls always observe a consistent view of the hook set and config.
//
// Lifecycle:
//
//	p := New(cfg, logger)
//	p.AddPre(...) ; p.AddPost(...)
//	// ... later, from many goroutines:
//	p.Run(ctx, agent, task, deps)
//	// ... on shutdown:
//	p.DrainAndClose(5 * time.Second)
type Pipeline struct {
	// snap is the active snapshot. Swapped atomically by AddPre,
	// AddPost, and SwapConfig. Run reads it once at the top and uses
	// that snapshot for the entire invocation.
	snap atomic.Pointer[snapshot]

	state stateMachine

	// inFlight counts active Run() invocations. Used by DrainAndClose
	// to wait for in-progress calls to complete.
	inFlight atomic.Int64

	logger  *zap.Logger
	metrics *PipelineMetrics

	// breakers is an immutable (copy-on-grow) map from hook name to
	// circuit breaker. We build it via atomic.Pointer to match the
	// snapshot pattern — no locks on the hot path.
	breakers atomic.Pointer[breakerMap]

	// hookTimeout caps per-hook execution when the hook itself doesn't
	// enforce a tighter ctx deadline.
	hookTimeout time.Duration
}

// breakerMap is just a typed alias so atomic.Pointer generics can
// carry the map type.
type breakerMap map[string]*breaker

// New creates a Pipeline with the given config. nil logger is upgraded
// to a no-op logger so the caller never has to nil-check.
func New(cfg Config, logger *zap.Logger) *Pipeline {
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &Pipeline{
		logger:      logger,
		metrics:     getPipelineMetrics(),
		hookTimeout: defaultHookTimeout,
	}
	p.snap.Store(&snapshot{cfg: cfg})
	bm := breakerMap{}
	p.breakers.Store(&bm)
	p.metrics.Generation.Set(float64(p.snap.Load().generation))
	return p
}

// AddPre registers a PreHook. Priority (via optional Prioritized
// interface) determines execution order; ties break by insertion
// order. Safe to call from any goroutine at any time, including
// while Run() is in flight — the change is visible to subsequent
// Run() calls only.
func (p *Pipeline) AddPre(h PreHook) *Pipeline {
	for {
		cur := p.snap.Load()
		next := cur.withPre(h)
		if p.snap.CompareAndSwap(cur, next) {
			p.ensureBreaker(h.Name())
			p.metrics.Generation.Set(float64(next.generation))
			p.logger.Debug("quality pipeline: pre-hook registered",
				zap.String("hook", h.Name()),
				zap.Int("priority", priorityOf(h)),
				zap.Uint64("generation", next.generation))
			return p
		}
		// CAS lost the race — retry with the new current.
	}
}

// AddPost registers a PostHook. Same semantics as AddPre.
func (p *Pipeline) AddPost(h PostHook) *Pipeline {
	for {
		cur := p.snap.Load()
		next := cur.withPost(h)
		if p.snap.CompareAndSwap(cur, next) {
			p.ensureBreaker(h.Name())
			p.metrics.Generation.Set(float64(next.generation))
			p.logger.Debug("quality pipeline: post-hook registered",
				zap.String("hook", h.Name()),
				zap.Int("priority", priorityOf(h)),
				zap.Uint64("generation", next.generation))
			return p
		}
	}
}

// SwapConfig atomically replaces the Config while preserving the
// current hook set. Enables hot reload without rebuilding the whole
// pipeline. In-flight Run() calls keep the old Config (correct —
// one turn runs under one config).
func (p *Pipeline) SwapConfig(cfg Config) {
	for {
		cur := p.snap.Load()
		next := cur.withConfig(cfg)
		if p.snap.CompareAndSwap(cur, next) {
			p.metrics.Generation.Set(float64(next.generation))
			p.logger.Info("quality pipeline: config swapped",
				zap.Uint64("generation", next.generation))
			return
		}
	}
}

// Config returns the live config snapshot. Returned by value.
func (p *Pipeline) Config() Config { return p.snap.Load().cfg }

// Generation returns the current snapshot generation. Monotonically
// increasing; useful for tests and logs to correlate a Run with a
// specific pipeline version.
func (p *Pipeline) Generation() uint64 { return p.snap.Load().generation }

// State returns the observable pipeline state.
func (p *Pipeline) State() PipelineState { return p.state.Load() }

// HookCounts reports how many pre and post hooks are registered. Used
// by /config quality so the user can see at a glance whether hooks
// are actually wired (a non-zero count means a phase delivered its hook).
func (p *Pipeline) HookCounts() (pre, post int) {
	s := p.snap.Load()
	return len(s.pre), len(s.post)
}

// SetHookTimeout overrides the per-hook execution budget. Setting to
// 0 or negative keeps the default.
func (p *Pipeline) SetHookTimeout(d time.Duration) {
	if d > 0 {
		p.hookTimeout = d
	}
}

// DrainAndClose transitions the Pipeline to Draining, waits up to
// timeout for in-flight Run() calls to complete, then Closes. After
// Close, subsequent Run() invocations return ErrPipelineClosed. Does
// NOT cancel in-flight calls — the caller's ctx is the cancellation
// mechanism; this just stops accepting new work.
func (p *Pipeline) DrainAndClose(timeout time.Duration) {
	if !p.state.transition(StateActive, StateDraining) {
		// Already draining or closed — proceed to close anyway so
		// double-calls are safe.
	}
	p.logger.Info("quality pipeline: draining",
		zap.Int64("in_flight", p.inFlight.Load()))

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.inFlight.Load() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.state.val.Store(int32(StateClosed))
	p.logger.Info("quality pipeline: closed",
		zap.Int64("in_flight_at_close", p.inFlight.Load()))
}

// Run wraps agent.Execute with the registered hooks.
//
// Order:
//  1. PreHooks (priority-ordered; each may rewrite task).
//     - A hook returning ErrSkipExecution short-circuits the whole
//       agent.Execute step and jumps to PostHooks with a synthesized
//       result (output taken from HookContext.SetShortCircuit if set).
//     - A hook returning ErrSkipRemainingHooks stops further PreHooks
//       but still runs agent.Execute and PostHooks.
//  2. agent.Execute(rewrittenTask)
//  3. PostHooks (priority-ordered; each may mutate result).
//     - ErrSkipRemainingHooks stops further PostHooks.
//
// When the pipeline master switch is off (cfg.Enabled == false), the
// pipeline degenerates to a direct agent.Execute call — preserving
// the "no-pipeline" performance contract for users who explicitly
// disable it.
//
// Per-hook isolation: every hook runs inside runHookIsolated, which
// recovers from panics, enforces hookTimeout, and records failures
// against a circuit breaker. A broken hook never takes down the
// rest of the pipeline or the user's turn.
func (p *Pipeline) Run(
	ctx context.Context,
	agent workers.WorkerAgent,
	task string,
	deps *workers.WorkerDeps,
) (*workers.AgentResult, error) {
	switch p.state.Load() {
	case StateClosed:
		p.metrics.DispatchTotal.WithLabelValues("closed").Inc()
		return agent.Execute(ctx, task, deps)
	case StateDraining:
		// During drain, keep honoring the request but via direct path
		// — we don't want to invoke hooks against a shutting-down
		// pipeline. This matches the "graceful degradation" contract.
		p.metrics.DispatchTotal.WithLabelValues("draining").Inc()
		return agent.Execute(ctx, task, deps)
	}

	p.inFlight.Add(1)
	defer p.inFlight.Add(-1)

	// Grab the snapshot once — any AddPre/AddPost/SwapConfig between
	// this line and the end of Run affects only future calls.
	s := p.snap.Load()

	if !s.cfg.Enabled {
		p.metrics.DispatchTotal.WithLabelValues("bypass_disabled").Inc()
		return agent.Execute(ctx, task, deps)
	}

	// Phase 1 (#7): cross-provider reasoning auto-enable. Attaches an
	// effort hint to ctx for agents in cfg.Reasoning.AutoAgents when no
	// hint is already present.
	ctx = applyAutoReasoning(ctx, s.cfg.Reasoning, agent)

	hc := &HookContext{
		Agent:  agent,
		Task:   task,
		Deps:   deps,
		Config: s.cfg,
	}

	currentTask := task
	skipExecute := false
	for _, h := range s.pre {
		newTask, err := p.runPreHookIsolated(ctx, h, hc)
		if errors.Is(err, ErrSkipExecution) {
			skipExecute = true
			break
		}
		if errors.Is(err, ErrSkipRemainingHooks) {
			break
		}
		if err != nil {
			continue
		}
		if newTask != "" {
			currentTask = newTask
			hc.Task = newTask
		}
	}

	var result *workers.AgentResult
	var execErr error
	if skipExecute {
		p.metrics.DispatchTotal.WithLabelValues("pre_short_circuit").Inc()
		out, _ := hc.ShortCircuitOutput()
		result = &workers.AgentResult{
			CallID: "",
			Agent:  agent.Type(),
			Task:   currentTask,
			Output: out,
		}
	} else {
		result, execErr = agent.Execute(ctx, currentTask, deps)
		if result == nil {
			result = &workers.AgentResult{}
		}
	}

	for _, h := range s.post {
		if err := p.runPostHookIsolated(ctx, h, hc, result); err != nil {
			if errors.Is(err, ErrSkipRemainingHooks) {
				break
			}
		}
	}

	if execErr != nil {
		p.metrics.DispatchTotal.WithLabelValues("exec_error").Inc()
	} else if !skipExecute {
		p.metrics.DispatchTotal.WithLabelValues("ok").Inc()
	}
	return result, execErr
}

// ─── per-hook isolation ───────────────────────────────────────────────────

// runPreHookIsolated runs a single PreHook under panic recovery,
// timeout, and circuit breaker protection. Returns the hook's
// proposed new task and any error (including sentinel controls).
func (p *Pipeline) runPreHookIsolated(ctx context.Context, h PreHook, hc *HookContext) (newTask string, err error) {
	name := h.Name()
	br := p.getBreaker(name)
	if !br.Allow() {
		p.metrics.HookErrors.WithLabelValues(name, "circuit_open").Inc()
		p.metrics.HookCircuitState.WithLabelValues(name).Set(float64(br.State()))
		p.logger.Warn("quality pipeline: pre-hook skipped (circuit open)",
			zap.String("hook", name))
		return "", nil
	}

	hookCtx, cancel := context.WithTimeout(ctx, p.hookTimeout)
	defer cancel()

	resultCh := make(chan preResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				p.logger.Error("quality pipeline: pre-hook panicked",
					zap.String("hook", name),
					zap.Any("panic", r),
					zap.Stack("stack"))
				resultCh <- preResult{err: fmt.Errorf("pre-hook %q panicked: %v", name, r), panicked: true}
			}
		}()
		start := time.Now()
		nt, e := h.PreRun(hookCtx, hc)
		p.metrics.HookDuration.WithLabelValues(name, "pre").Observe(time.Since(start).Seconds())
		resultCh <- preResult{newTask: nt, err: e}
	}()

	select {
	case r := <-resultCh:
		p.recordHookOutcome(br, name, r.err, r.panicked, false)
		return r.newTask, r.err
	case <-hookCtx.Done():
		p.recordHookOutcome(br, name, hookCtx.Err(), false, true)
		p.logger.Warn("quality pipeline: pre-hook timed out",
			zap.String("hook", name), zap.Duration("budget", p.hookTimeout))
		return "", nil
	}
}

// runPostHookIsolated runs a single PostHook under the same isolation
// guarantees as pre-hooks.
func (p *Pipeline) runPostHookIsolated(ctx context.Context, h PostHook, hc *HookContext, result *workers.AgentResult) error {
	name := h.Name()
	br := p.getBreaker(name)
	if !br.Allow() {
		p.metrics.HookErrors.WithLabelValues(name, "circuit_open").Inc()
		p.metrics.HookCircuitState.WithLabelValues(name).Set(float64(br.State()))
		return nil
	}

	hookCtx, cancel := context.WithTimeout(ctx, p.hookTimeout)
	defer cancel()

	errCh := make(chan postResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				p.logger.Error("quality pipeline: post-hook panicked",
					zap.String("hook", name),
					zap.Any("panic", r),
					zap.Stack("stack"))
				errCh <- postResult{err: fmt.Errorf("post-hook %q panicked: %v", name, r), panicked: true}
			}
		}()
		start := time.Now()
		e := h.PostRun(hookCtx, hc, result)
		p.metrics.HookDuration.WithLabelValues(name, "post").Observe(time.Since(start).Seconds())
		errCh <- postResult{err: e}
	}()

	select {
	case r := <-errCh:
		p.recordHookOutcome(br, name, r.err, r.panicked, false)
		return r.err
	case <-hookCtx.Done():
		p.recordHookOutcome(br, name, hookCtx.Err(), false, true)
		p.logger.Warn("quality pipeline: post-hook timed out",
			zap.String("hook", name), zap.Duration("budget", p.hookTimeout))
		return nil
	}
}

type preResult struct {
	newTask  string
	err      error
	panicked bool
}

type postResult struct {
	err      error
	panicked bool
}

// recordHookOutcome updates breaker + metrics based on the hook's
// outcome. Sentinel control errors (SkipExecution / SkipRemaining)
// count as success for breaker purposes — they're legitimate signals,
// not failures.
func (p *Pipeline) recordHookOutcome(br *breaker, name string, err error, panicked, timedOut bool) {
	switch {
	case panicked:
		p.metrics.HookErrors.WithLabelValues(name, "panic").Inc()
		br.RecordFailure()
	case timedOut:
		p.metrics.HookErrors.WithLabelValues(name, "timeout").Inc()
		br.RecordFailure()
	case err != nil && !errors.Is(err, ErrSkipExecution) && !errors.Is(err, ErrSkipRemainingHooks):
		p.metrics.HookErrors.WithLabelValues(name, "returned_error").Inc()
		br.RecordFailure()
	default:
		br.RecordSuccess()
	}
	p.metrics.HookCircuitState.WithLabelValues(name).Set(float64(br.State()))
}

// ensureBreaker registers a circuit breaker for name if one doesn't
// already exist. CAS loop over the breaker-map atomic pointer.
func (p *Pipeline) ensureBreaker(name string) {
	for {
		cur := p.breakers.Load()
		if _, ok := (*cur)[name]; ok {
			return
		}
		next := make(breakerMap, len(*cur)+1)
		for k, v := range *cur {
			next[k] = v
		}
		next[name] = newBreaker(DefaultBreakerConfig())
		if p.breakers.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// getBreaker returns the breaker for name, creating one on demand if
// the hook is somehow invoked without having been registered (defense
// in depth).
func (p *Pipeline) getBreaker(name string) *breaker {
	cur := p.breakers.Load()
	if b, ok := (*cur)[name]; ok {
		return b
	}
	p.ensureBreaker(name)
	cur = p.breakers.Load()
	return (*cur)[name]
}

// Compile-time assertion: Pipeline satisfies workers.ExecutionPipeline.
var _ workers.ExecutionPipeline = (*Pipeline)(nil)
