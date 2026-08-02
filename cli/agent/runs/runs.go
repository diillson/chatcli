/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package runs is the process-wide registry of live agent executions.
 *
 * Every agent execution — orchestrator loop, dispatched worker, recursive
 * subagent, MoA panel member, scheduler headless run — registers itself here
 * at start and reports coarse progress (current turn, current action, tool
 * call count) while it runs. The registry is the single source of truth for
 * "which agents are alive right now and what is each one doing", consumed by
 * the live dispatch panel, the /agents REPL command and the @agents tool.
 *
 * The package is a leaf: stdlib only, importable from cli, cli/agent/workers,
 * cli/metrics and cli/plugins without cycles. All exported types are safe for
 * concurrent use; *Run methods are nil-safe so instrumentation call sites can
 * update progress unconditionally.
 */
package runs

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Kind classifies what spawned the agent execution.
type Kind string

const (
	// KindOrchestrator is the main agent/coder ReAct loop.
	KindOrchestrator Kind = "orchestrator"
	// KindWorker is a specialized agent dispatched via <agent_call>.
	KindWorker Kind = "worker"
	// KindSubagent is a recursive delegate spawned by a worker or the loop.
	KindSubagent Kind = "subagent"
	// KindMoA is a Mixture-of-Agents panel member.
	KindMoA Kind = "moa"
	// KindHeadless is a scheduler-driven run without a TTY.
	KindHeadless Kind = "headless"
)

// Status is the lifecycle state of an agent execution.
type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Terminal reports whether the status is a final state.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// Info is an immutable snapshot of one agent execution. Values returned by
// the registry are copies — callers may retain them freely.
type Info struct {
	ID        string
	CallID    string // dispatcher <agent_call> ID when applicable
	Kind      Kind
	Agent     string // agent type name: "coder", "reviewer", ...
	Task      string
	ParentID  string
	Origin    string // "repl", "gateway", "scheduler", "mcp", "acp"
	Status    Status
	Turn      int
	MaxTurns  int
	Action    string // current action label, e.g. "read cli/foo.go"
	ToolCalls int
	StartedAt time.Time
	EndedAt   time.Time
	Err       string
}

// Elapsed returns the run's wall-clock duration so far (or total when done).
func (i Info) Elapsed() time.Duration {
	if !i.EndedAt.IsZero() {
		return i.EndedAt.Sub(i.StartedAt)
	}
	return time.Since(i.StartedAt)
}

// Run is a live handle to one registered execution. The owning goroutine
// updates progress through it; observers read snapshots via the registry.
// All methods are nil-safe no-ops so instrumentation never needs nil checks.
type Run struct {
	mu     sync.Mutex
	info   Info
	cancel context.CancelFunc
	reg    *Registry
}

// ID returns the run's registry ID ("" on nil).
func (r *Run) ID() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info.ID
}

// SetTurn records the current ReAct turn (1-based) and the turn budget.
func (r *Run) SetTurn(turn, maxTurns int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info.Turn = turn
	r.info.MaxTurns = maxTurns
}

// SetAction records the human-readable label of the action in flight.
func (r *Run) SetAction(action string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info.Action = action
}

// AddToolCalls bumps the executed tool call counter by n.
func (r *Run) AddToolCalls(n int) {
	if r == nil || n <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info.ToolCalls += n
}

// Snapshot returns a copy of the run's current state (zero Info on nil).
func (r *Run) Snapshot() Info {
	if r == nil {
		return Info{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.info
}

// End finalizes the run: status derives from err (nil → completed,
// context.Canceled → cancelled, otherwise failed) and the run moves from the
// active set to the recent-history ring. End is idempotent.
func (r *Run) End(err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.info.Status.Terminal() {
		r.mu.Unlock()
		return
	}
	r.info.EndedAt = time.Now()
	switch {
	case err == nil:
		r.info.Status = StatusCompleted
	case errors.Is(err, context.Canceled):
		r.info.Status = StatusCancelled
		r.info.Err = err.Error()
	default:
		r.info.Status = StatusFailed
		r.info.Err = err.Error()
	}
	final := r.info
	reg := r.reg
	r.mu.Unlock()

	if reg != nil {
		reg.retire(final)
	}
}

// DefaultHistorySize is how many finished runs the registry retains for
// /agents and @agents inspection. Overridable via CHATCLI_AGENT_RUNS_HISTORY.
const DefaultHistorySize = 200

// Registry tracks live and recently finished agent executions.
type Registry struct {
	mu      sync.RWMutex
	active  map[string]*Run
	order   []string // active runs in start order
	done    []Info   // ring of finished runs, oldest first
	doneCap int
	seq     atomic.Uint64
}

// NewRegistry builds an empty registry with the given history capacity
// (<=0 falls back to DefaultHistorySize).
func NewRegistry(historyCap int) *Registry {
	if historyCap <= 0 {
		historyCap = DefaultHistorySize
	}
	return &Registry{
		active:  make(map[string]*Run),
		doneCap: historyCap,
	}
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default returns the process-wide registry, sized from
// CHATCLI_AGENT_RUNS_HISTORY on first use.
func Default() *Registry {
	defaultOnce.Do(func() {
		capacity := DefaultHistorySize
		if v := os.Getenv("CHATCLI_AGENT_RUNS_HISTORY"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				capacity = n
			}
		}
		defaultReg = NewRegistry(capacity)
	})
	return defaultReg
}

// Begin registers a new execution and returns a derived context plus the live
// handle. The context carries the handle (FromContext) and is cancellable via
// Registry.Cancel(id). When meta.ParentID is empty and ctx already carries a
// run, the new run is parented to it — that is how the subagent/worker tree
// forms without explicit threading.
func (g *Registry) Begin(ctx context.Context, meta Info) (context.Context, *Run) {
	if g == nil {
		return ctx, nil
	}
	if parent := FromContext(ctx); parent != nil {
		snap := parent.Snapshot()
		if meta.ParentID == "" {
			meta.ParentID = snap.ID
		}
		if meta.Origin == "" {
			meta.Origin = snap.Origin
		}
	}
	ctx, cancel := context.WithCancel(ctx)

	meta.ID = "run-" + strconv.FormatUint(g.seq.Add(1), 10)
	meta.Status = StatusRunning
	meta.StartedAt = time.Now()
	meta.EndedAt = time.Time{}

	run := &Run{info: meta, cancel: cancel, reg: g}

	g.mu.Lock()
	g.active[meta.ID] = run
	g.order = append(g.order, meta.ID)
	g.mu.Unlock()

	return WithRun(ctx, run), run
}

// retire moves a finished run out of the active set into the history ring.
func (g *Registry) retire(final Info) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.active[final.ID]; ok {
		delete(g.active, final.ID)
		for i, id := range g.order {
			if id == final.ID {
				g.order = append(g.order[:i], g.order[i+1:]...)
				break
			}
		}
	}
	g.done = append(g.done, final)
	if overflow := len(g.done) - g.doneCap; overflow > 0 {
		g.done = append(g.done[:0], g.done[overflow:]...)
	}
}

// Active returns snapshots of all live runs in start order.
func (g *Registry) Active() []Info {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Info, 0, len(g.order))
	for _, id := range g.order {
		if run, ok := g.active[id]; ok {
			out = append(out, run.Snapshot())
		}
	}
	return out
}

// Recent returns up to n finished runs, newest first. n<=0 means all retained.
func (g *Registry) Recent(n int) []Info {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	total := len(g.done)
	if n <= 0 || n > total {
		n = total
	}
	out := make([]Info, 0, n)
	for i := total - 1; i >= total-n; i-- {
		out = append(out, g.done[i])
	}
	return out
}

// Get returns a snapshot of one run — live or finished — by ID.
func (g *Registry) Get(id string) (Info, bool) {
	if g == nil || id == "" {
		return Info{}, false
	}
	g.mu.RLock()
	if run, ok := g.active[id]; ok {
		g.mu.RUnlock()
		return run.Snapshot(), true
	}
	for i := len(g.done) - 1; i >= 0; i-- {
		if g.done[i].ID == id {
			info := g.done[i]
			g.mu.RUnlock()
			return info, true
		}
	}
	g.mu.RUnlock()
	return Info{}, false
}

// ByCallID returns the live run registered for a dispatcher call ID.
func (g *Registry) ByCallID(callID string) (Info, bool) {
	if g == nil || callID == "" {
		return Info{}, false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, run := range g.active {
		if snap := run.Snapshot(); snap.CallID == callID {
			return snap, true
		}
	}
	return Info{}, false
}

// Children returns snapshots of live runs parented to id, in start order.
func (g *Registry) Children(id string) []Info {
	if g == nil || id == "" {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []Info
	for _, rid := range g.order {
		if run, ok := g.active[rid]; ok {
			if snap := run.Snapshot(); snap.ParentID == id {
				out = append(out, snap)
			}
		}
	}
	return out
}

// Cancel requests cancellation of a live run (and, through context
// propagation, of everything it spawned). Returns false when the run is not
// live. The run stays in the active set until its goroutine observes the
// cancellation and calls End.
func (g *Registry) Cancel(id string) bool {
	if g == nil || id == "" {
		return false
	}
	g.mu.RLock()
	run, ok := g.active[id]
	g.mu.RUnlock()
	if !ok {
		return false
	}
	run.mu.Lock()
	cancel := run.cancel
	run.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
}

// All returns active runs (start order) followed by recent finished runs
// (newest first, capped at recentN). Convenience for list views.
func (g *Registry) All(recentN int) []Info {
	out := g.Active()
	return append(out, g.Recent(recentN)...)
}

// SortByStart orders a snapshot slice by start time ascending (stable view
// for panels that merge active and finished runs).
func SortByStart(infos []Info) {
	sort.SliceStable(infos, func(i, j int) bool {
		return infos[i].StartedAt.Before(infos[j].StartedAt)
	})
}

type ctxKey struct{}

// WithRun attaches a live run handle to the context.
func WithRun(ctx context.Context, r *Run) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, r)
}

// FromContext retrieves the run handle attached to ctx, or nil.
func FromContext(ctx context.Context) *Run {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(ctxKey{}); v != nil {
		if r, ok := v.(*Run); ok {
			return r
		}
	}
	return nil
}
