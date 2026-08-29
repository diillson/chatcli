/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * The engine is the deterministic orchestrator of one run: it owns every
 * state transition, schedules tasks the moment their dependencies complete
 * (ready-set scheduling, not level barriers), runs validation gates itself,
 * and only promotes a task to done on an independent reviewer's verdict.
 * LLM workers execute and review; they never write graph state.
 */
package taskgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Event is one line of the run's append-only trail (and the live feed).
type Event struct {
	TS     time.Time `json:"ts"`
	Task   string    `json:"task,omitempty"`
	Type   string    `json:"event"`
	Detail string    `json:"detail,omitempty"`
}

// Event types emitted by the engine.
const (
	EventRunStarted    = "run_started"
	EventTaskStarted   = "task_started"
	EventGateResult    = "gate_result"
	EventReviewStarted = "review_started"
	EventReviewVerdict = "review_verdict"
	EventTaskRetry     = "task_retry"
	EventTaskDone      = "task_done"
	EventTaskFailed    = "task_failed"
	EventTaskBlocked   = "task_blocked"
	EventRunFinished   = "run_finished"
)

// StepDispatcher is the slice of workers.Dispatcher the engine needs —
// identical to quality.StepDispatcher so the live dispatcher satisfies both.
type StepDispatcher interface {
	Dispatch(ctx context.Context, calls []workers.AgentCall) []workers.AgentResult
}

// GateRunner executes one validation command inside the workspace (sandboxed,
// unsafe-command-gated) and returns its combined output. A non-nil error
// means the command failed (non-zero exit, timeout, or refusal).
type GateRunner interface {
	RunGateStep(ctx context.Context, dir, cmd string, onLine func(string)) (string, error)
}

// Config wires the engine's collaborators. Everything model- or
// session-specific is injected so the package stays decoupled from cli.
type Config struct {
	Dispatcher  StepDispatcher
	Gate        GateRunner
	MaxParallel int    // <=0 → DefaultMaxParallel
	Workspace   string // workspace root for gates and checkpoints
	// Checkpoint snapshots the workspace before an executor runs
	// (best-effort; nil disables).
	Checkpoint func(label string)
	// CostPerCall converts one LLM call's usage into USD (the session's
	// pricing tables live in the cli package). Nil disables attribution.
	CostPerCall func(provider, model string, usage *models.UsageInfo) float64
	// OnEvent receives every event after it is persisted (terminal
	// streaming). Nil disables.
	OnEvent func(Event)
	Logger  *zap.Logger
}

// DefaultMaxParallel bounds concurrent executor tasks when neither the graph
// nor the config says otherwise.
const DefaultMaxParallel = 4

const (
	// outputTailRunes bounds stored executor/gate outputs: the tail is what
	// matters (test verdicts, final errors) and state.json must stay small.
	outputTailRunes = 2000
	// depContextHeadRunes bounds a dependency output substituted into a
	// successor's prompt via the #<taskID> reference.
	depContextHeadRunes = 2000
	// gateStepTimeout bounds one validation command.
	gateStepTimeout = 30 * time.Minute
)

// Engine executes one graph. Use NewEngine per run; instances are not
// reusable.
type Engine struct {
	graph *Graph
	store *RunStore
	cfg   Config

	mu      sync.Mutex // guards graph state, outputs and cost attribution
	outputs map[string]string
	// callCosts maps in-flight dispatch call IDs to their task, so
	// RecordCallUsage can attribute spend from the shared dispatcher
	// stream while ignoring calls the engine did not start.
	callTask map[string]string

	cancelMu sync.Mutex
	cancel   context.CancelFunc
}

// NewEngine builds an engine over a loaded graph and its store.
func NewEngine(g *Graph, store *RunStore, cfg Config) (*Engine, error) {
	if g == nil || store == nil {
		return nil, errors.New("taskgraph engine requires a graph and a store")
	}
	if cfg.Dispatcher == nil {
		return nil, errors.New("taskgraph engine requires a dispatcher")
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = DefaultMaxParallel
	}
	if g.MaxParallel > 0 && g.MaxParallel < cfg.MaxParallel {
		cfg.MaxParallel = g.MaxParallel
	}
	return &Engine{
		graph:    g,
		store:    store,
		cfg:      cfg,
		outputs:  make(map[string]string),
		callTask: make(map[string]string),
	}, nil
}

// RecordCallUsage attributes one worker LLM call's cost to the task that
// spawned it. Calls with unknown IDs belong to other dispatch waves on the
// shared dispatcher and are ignored. Safe for concurrent use.
func (e *Engine) RecordCallUsage(callID, provider, model string, usage *models.UsageInfo) {
	if usage == nil || e.cfg.CostPerCall == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	taskID, ok := e.callTask[callID]
	if !ok {
		return
	}
	cost := e.cfg.CostPerCall(provider, model, usage)
	if cost <= 0 {
		return
	}
	if t := e.graph.TaskByID(taskID); t != nil {
		t.CostUSD += cost
		if n := len(t.Attempts); n > 0 {
			t.Attempts[n-1].CostUSD += cost
		}
	}
}

// Cancel requests the run to stop. Idempotent.
func (e *Engine) Cancel() {
	e.cancelMu.Lock()
	defer e.cancelMu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
}

// Run executes the graph until every task is terminal or ctx is canceled,
// then returns the final report. Resume is inherent: tasks already done stay
// done, so re-running a loaded graph continues where it stopped.
func (e *Engine) Run(ctx context.Context) (string, error) {
	runCtx, cancel := context.WithCancel(ctx)
	e.cancelMu.Lock()
	e.cancel = cancel
	e.cancelMu.Unlock()
	defer cancel()

	// Register the run-parent in the process-wide registry so /agents and
	// the Hub bridge observe it; workers register themselves as children.
	regCtx, liveRun := runs.Default().Begin(runCtx, runs.Info{
		Kind:  runs.KindTaskGraph,
		Agent: "taskgraph",
		Task:  e.graph.Name,
	})

	e.mu.Lock()
	e.graph.Status = StatusRunning
	if e.graph.StartedAt.IsZero() {
		e.graph.StartedAt = time.Now()
	}
	for _, t := range e.graph.Tasks {
		if t.Status == StatusDone {
			e.outputs[t.ID] = lastExecutorOutput(t)
		}
	}
	e.persistLocked()
	e.mu.Unlock()
	e.emit(Event{Type: EventRunStarted, Detail: e.graph.Name})

	type doneMsg struct{ id string }
	doneCh := make(chan doneMsg)
	inFlight := 0

	for {
		if regCtx.Err() == nil {
			for _, t := range e.readyTasks() {
				if inFlight >= e.cfg.MaxParallel {
					break
				}
				task := t
				e.setStatus(task, StatusRunning, EventTaskStarted, task.Title)
				inFlight++
				go func() {
					e.runTask(regCtx, task)
					doneCh <- doneMsg{id: task.ID}
				}()
			}
		}
		if inFlight == 0 {
			break
		}
		msg := <-doneCh
		inFlight--
		e.propagateBlocked(msg.id)
	}

	e.mu.Lock()
	e.graph.EndedAt = time.Now()
	counts := e.graph.CountByStatus()
	switch {
	case regCtx.Err() != nil:
		e.graph.Status = StatusFailed
	case counts[StatusFailed] > 0 || counts[StatusBlocked] > 0 || counts[StatusPending] > 0:
		e.graph.Status = StatusFailed
	default:
		e.graph.Status = StatusDone
	}
	finalStatus := e.graph.Status
	e.persistLocked()
	e.mu.Unlock()

	liveRun.End(regCtx.Err())
	e.emit(Event{Type: EventRunFinished, Detail: string(finalStatus)})
	report := FormatRunReport(e.graph)
	if err := regCtx.Err(); err != nil {
		return report, fmt.Errorf("task graph run canceled: %w", err)
	}
	return report, nil
}

// readyTasks returns pending tasks whose deps are all done.
func (e *Engine) readyTasks() []*Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*Task
	for _, t := range e.graph.Tasks {
		if t.Status != StatusPending {
			continue
		}
		ready := true
		for _, dep := range t.Deps {
			if d := e.graph.TaskByID(dep); d == nil || d.Status != StatusDone {
				ready = false
				break
			}
		}
		if ready {
			out = append(out, t)
		}
	}
	return out
}

// propagateBlocked marks every pending task downstream of a failed/blocked
// dependency as blocked, transitively.
func (e *Engine) propagateBlocked(_ string) {
	for {
		var blocked *Task
		e.mu.Lock()
		for _, t := range e.graph.Tasks {
			if t.Status != StatusPending {
				continue
			}
			for _, dep := range t.Deps {
				if d := e.graph.TaskByID(dep); d != nil && (d.Status == StatusFailed || d.Status == StatusBlocked) {
					blocked = t
					break
				}
			}
			if blocked != nil {
				break
			}
		}
		e.mu.Unlock()
		if blocked == nil {
			return
		}
		e.setStatus(blocked, StatusBlocked, EventTaskBlocked, "dependency failed")
	}
}

// runTask drives one task through its attempt loop:
// executor → gate → reviewer, retrying with the reviewer's feedback until
// PASS or MaxAttempts.
func (e *Engine) runTask(ctx context.Context, t *Task) {
	e.mu.Lock()
	if t.StartedAt.IsZero() {
		t.StartedAt = time.Now()
	}
	startAttempt := len(t.Attempts)
	e.mu.Unlock()

	feedback := ""
	for n := startAttempt + 1; n <= t.MaxAttempts; n++ {
		if ctx.Err() != nil {
			e.failTask(t, "run canceled")
			return
		}
		attempt := e.beginAttempt(t, n)
		if e.cfg.Checkpoint != nil {
			e.cfg.Checkpoint("taskgraph:" + t.ID)
		}

		execRes := e.dispatchExecutor(ctx, t, n, feedback)
		e.mu.Lock()
		attempt.ExecutorRunID = execRes.Metadata["run_id"]
		attempt.ExecutorOutput = tailRunes(execRes.Output, outputTailRunes)
		e.persistLocked()
		e.mu.Unlock()
		if execRes.Error != nil {
			feedback = "executor error: " + execRes.Error.Error()
			e.endAttempt(t, attempt, feedback)
			continue
		}

		gateOK, gateFeedback := e.runGate(ctx, t, attempt)
		if !gateOK {
			feedback = gateFeedback
			e.endAttempt(t, attempt, feedback)
			continue
		}

		if !e.graph.RequiresReview(t) {
			e.finishTask(t, attempt, reviewWaivedByGraph, execRes.Output)
			return
		}

		e.setStatus(t, StatusReviewing, EventReviewStarted, fmt.Sprintf("attempt %d", n))
		verdict, evidence, reviewRunID := e.dispatchReviewer(ctx, t, n, attempt, execRes.Output)
		e.mu.Lock()
		attempt.ReviewerRunID = reviewRunID
		attempt.Verdict = verdict
		attempt.Evidence = evidence
		e.persistLocked()
		e.mu.Unlock()
		e.emit(Event{Task: t.ID, Type: EventReviewVerdict, Detail: verdict + " — " + firstLine(evidence)})

		if verdict == verdictPass {
			e.finishTask(t, attempt, evidence, execRes.Output)
			return
		}
		feedback = evidence
		e.endAttempt(t, attempt, "review FAIL: "+evidence)
	}
	e.failTask(t, fmt.Sprintf("exhausted %d attempts; last feedback: %s", t.MaxAttempts, firstLine(feedback)))
}

// beginAttempt appends a new attempt record and returns it (pointer into the
// task's slice — mutations must hold e.mu).
func (e *Engine) beginAttempt(t *Task, n int) *Attempt {
	e.mu.Lock()
	defer e.mu.Unlock()
	t.Status = StatusRunning
	t.Attempts = append(t.Attempts, Attempt{N: n, StartedAt: time.Now()})
	e.persistLocked()
	return &t.Attempts[len(t.Attempts)-1]
}

// endAttempt closes a failing attempt and emits the retry event (unless the
// attempt budget is exhausted — runTask handles the terminal transition).
func (e *Engine) endAttempt(t *Task, a *Attempt, reason string) {
	e.mu.Lock()
	a.FailureReason = tailRunes(reason, outputTailRunes)
	a.EndedAt = time.Now()
	last := a.N >= t.MaxAttempts
	e.persistLocked()
	e.mu.Unlock()
	if !last {
		e.emit(Event{Task: t.ID, Type: EventTaskRetry, Detail: firstLine(reason)})
	}
}

// finishTask promotes a task to done — the only code path that does.
func (e *Engine) finishTask(t *Task, a *Attempt, evidence, output string) {
	e.mu.Lock()
	a.EndedAt = time.Now()
	t.Status = StatusDone
	t.EndedAt = time.Now()
	e.outputs[t.ID] = strings.TrimSpace(output)
	e.persistLocked()
	e.mu.Unlock()
	e.emit(Event{Task: t.ID, Type: EventTaskDone, Detail: firstLine(evidence)})
}

// failTask marks a task terminally failed.
func (e *Engine) failTask(t *Task, reason string) {
	e.mu.Lock()
	t.Status = StatusFailed
	t.EndedAt = time.Now()
	if n := len(t.Attempts); n > 0 && t.Attempts[n-1].EndedAt.IsZero() {
		t.Attempts[n-1].EndedAt = time.Now()
		t.Attempts[n-1].FailureReason = tailRunes(reason, outputTailRunes)
	}
	e.persistLocked()
	e.mu.Unlock()
	e.emit(Event{Task: t.ID, Type: EventTaskFailed, Detail: firstLine(reason)})
}

// setStatus applies a non-terminal transition and emits its event.
func (e *Engine) setStatus(t *Task, s Status, eventType, detail string) {
	e.mu.Lock()
	t.Status = s
	e.persistLocked()
	e.mu.Unlock()
	e.emit(Event{Task: t.ID, Type: eventType, Detail: detail})
}

// dispatchExecutor sends the task to its worker agent and returns the result.
func (e *Engine) dispatchExecutor(ctx context.Context, t *Task, attempt int, feedback string) workers.AgentResult {
	callID := fmt.Sprintf("tg:%s:e%d", t.ID, attempt)
	e.trackCall(callID, t.ID)
	defer e.untrackCall(callID)
	prompt := e.buildExecutorPrompt(t, attempt, feedback)
	batch := e.cfg.Dispatcher.Dispatch(ctx, []workers.AgentCall{{
		Agent: workers.AgentType(t.Agent),
		Task:  prompt,
		ID:    callID,
	}})
	if len(batch) == 0 {
		return workers.AgentResult{CallID: callID, Error: errors.New("dispatcher returned no result")}
	}
	return batch[0]
}

// trackCall / untrackCall maintain the callID→task map for cost attribution.
func (e *Engine) trackCall(callID, taskID string) {
	e.mu.Lock()
	e.callTask[callID] = taskID
	e.mu.Unlock()
}

func (e *Engine) untrackCall(callID string) {
	e.mu.Lock()
	delete(e.callTask, callID)
	e.mu.Unlock()
}

// persistLocked saves state best-effort; the run never aborts on a save
// failure. Caller must hold e.mu.
func (e *Engine) persistLocked() {
	if err := e.store.SaveState(e.graph); err != nil {
		e.cfg.Logger.Warn("taskgraph state save failed", zap.Error(err))
	}
}

// emit persists the event (best-effort) then streams it.
func (e *Engine) emit(ev Event) {
	ev.TS = time.Now()
	if err := e.store.AppendEvent(ev); err != nil {
		e.cfg.Logger.Warn("taskgraph event append failed", zap.Error(err))
	}
	if e.cfg.OnEvent != nil {
		e.cfg.OnEvent(ev)
	}
}

// lastExecutorOutput recovers a done task's output for dependency
// substitution after a resume.
func lastExecutorOutput(t *Task) string {
	for i := len(t.Attempts) - 1; i >= 0; i-- {
		if out := strings.TrimSpace(t.Attempts[i].ExecutorOutput); out != "" {
			return out
		}
	}
	return ""
}

// tailRunes keeps the last n runes of s — the tail is where verdicts and
// final errors live.
func tailRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func headRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
