/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Live wiring of @taskgraph: resolves the session's worker dispatcher, runs
 * validation gates through the coder engine (sandboxed, unsafe-command
 * gated), attributes real per-call cost to graph tasks, and guards the
 * one-active-run-per-session invariant.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/taskgraph"
	"github.com/diillson/chatcli/models"
	coderengine "github.com/diillson/chatcli/pkg/coder/engine"
	"go.uber.org/zap"
)

// agentMaxWorkersEnv is the SQUAD's existing parallelism cap — the graph
// deliberately reuses it (no new env): concurrent tasks are concurrent
// workers, and one knob must govern both.
const agentMaxWorkersEnv = "CHATCLI_AGENT_MAX_WORKERS"

// gateStepTimeoutSeconds bounds one gate command inside the coder engine.
const gateStepTimeoutSeconds = 1800

// taskGraphDispatcher is the slice of *workers.Dispatcher the adapter needs:
// dispatching plus per-call cost attribution.
type taskGraphDispatcher interface {
	taskgraph.StepDispatcher
	SetCallUsageRecorder(fn workers.CallUsageRecorder)
}

// taskGraphAdapter implements plugins.TaskGraphAdapter over the live session.
type taskGraphAdapter struct {
	cli    *ChatCLI
	logger *zap.Logger
	// baseDirFn / dispatcherFn are test seams; nil means the live defaults.
	baseDirFn    func() (string, error)
	dispatcherFn func() (taskGraphDispatcher, error)

	mu     sync.Mutex
	active *taskgraph.Engine
}

// newTaskGraphAdapter builds the adapter.
func newTaskGraphAdapter(cli *ChatCLI, logger *zap.Logger) *taskGraphAdapter {
	return &taskGraphAdapter{cli: cli, logger: logger}
}

// dispatcher resolves the CURRENT agent dispatcher at call time — the
// AgentMode is re-created across sessions, so the reference must never be
// cached (same pattern as the task tracker provider).
func (a *taskGraphAdapter) dispatcher() (taskGraphDispatcher, error) {
	if a.dispatcherFn != nil {
		return a.dispatcherFn()
	}
	if a.cli.agentMode == nil || a.cli.agentMode.agentDispatcher == nil {
		return nil, errors.New("@taskgraph: agent dispatcher not available (run inside /coder)")
	}
	return a.cli.agentMode.agentDispatcher, nil
}

func (a *taskGraphAdapter) baseDir() (string, error) {
	if a.baseDirFn != nil {
		return a.baseDirFn()
	}
	return taskgraph.DefaultBaseDir()
}

// Plan implements plugins.TaskGraphAdapter.
func (a *taskGraphAdapter) Plan(graphJSON string) (string, error) {
	g, err := taskgraph.ParseGraph(graphJSON)
	if err != nil {
		return "", fmt.Errorf("@taskgraph plan: %w", err)
	}
	base, err := a.baseDir()
	if err != nil {
		return "", err
	}
	store, err := taskgraph.CreateRun(base, g)
	if err != nil {
		return "", fmt.Errorf("@taskgraph plan: %w", err)
	}
	return fmt.Sprintf("plan accepted: run %s (%d tasks). Execute with {\"cmd\":\"run\",\"args\":{\"id\":%q}}",
		store.RunID(), len(g.Tasks), store.RunID()), nil
}

// Run implements plugins.TaskGraphAdapter.
func (a *taskGraphAdapter) Run(ctx context.Context, runID, graphJSON string, onOutput func(string)) (string, error) {
	base, err := a.baseDir()
	if err != nil {
		return "", err
	}
	var (
		g     *taskgraph.Graph
		store *taskgraph.RunStore
	)
	switch {
	case strings.TrimSpace(graphJSON) != "":
		g, err = taskgraph.ParseGraph(graphJSON)
		if err != nil {
			return "", fmt.Errorf("@taskgraph run: %w", err)
		}
		store, err = taskgraph.CreateRun(base, g)
		if err != nil {
			return "", fmt.Errorf("@taskgraph run: %w", err)
		}
	default:
		store, g, err = a.loadRun(base, runID)
		if err != nil {
			return "", err
		}
		if g.Status == taskgraph.StatusDone {
			return taskgraph.FormatRunReport(g), nil
		}
	}
	return a.execute(ctx, g, store, onOutput)
}

// Retry implements plugins.TaskGraphAdapter: re-opens one failed task plus
// its blocked successors, extends the attempt budget by one, and resumes.
func (a *taskGraphAdapter) Retry(ctx context.Context, runID, taskID string, onOutput func(string)) (string, error) {
	base, err := a.baseDir()
	if err != nil {
		return "", err
	}
	store, g, err := a.loadRun(base, runID)
	if err != nil {
		return "", err
	}
	t := g.TaskByID(taskID)
	if t == nil {
		return "", fmt.Errorf("@taskgraph retry: no task %q in run %s", taskID, store.RunID())
	}
	if t.Status != taskgraph.StatusFailed {
		return "", fmt.Errorf("@taskgraph retry: task %s is %s (only failed tasks can be retried)", taskID, t.Status)
	}
	t.Status = taskgraph.StatusPending
	if len(t.Attempts) >= t.MaxAttempts {
		t.MaxAttempts = len(t.Attempts) + 1
	}
	for _, other := range g.Tasks {
		if other.Status == taskgraph.StatusBlocked {
			other.Status = taskgraph.StatusPending
		}
	}
	g.Status = taskgraph.StatusPending
	if err := store.SaveState(g); err != nil {
		return "", fmt.Errorf("@taskgraph retry: %w", err)
	}
	return a.execute(ctx, g, store, onOutput)
}

// Status implements plugins.TaskGraphAdapter.
func (a *taskGraphAdapter) Status(runID string) (string, error) {
	base, err := a.baseDir()
	if err != nil {
		return "", err
	}
	_, g, err := a.loadRun(base, runID)
	if err != nil {
		return "", err
	}
	return taskgraph.FormatRunReport(g), nil
}

// Show implements plugins.TaskGraphAdapter.
func (a *taskGraphAdapter) Show(runID, taskID string) (string, error) {
	base, err := a.baseDir()
	if err != nil {
		return "", err
	}
	store, g, err := a.loadRun(base, runID)
	if err != nil {
		return "", err
	}
	t := g.TaskByID(taskID)
	if t == nil {
		return "", fmt.Errorf("@taskgraph show: no task %q in run %s", taskID, store.RunID())
	}
	return taskgraph.FormatTaskDetail(g, t), nil
}

// Cancel implements plugins.TaskGraphAdapter.
func (a *taskGraphAdapter) Cancel() (string, error) {
	a.mu.Lock()
	engine := a.active
	a.mu.Unlock()
	if engine == nil {
		return "", errors.New("@taskgraph cancel: no active run")
	}
	engine.Cancel()
	return "cancellation requested; tasks end when they observe the signal", nil
}

// List implements plugins.TaskGraphAdapter.
func (a *taskGraphAdapter) List() (string, error) {
	base, err := a.baseDir()
	if err != nil {
		return "", err
	}
	rows, err := taskgraph.ListRuns(base)
	if err != nil {
		return "", fmt.Errorf("@taskgraph list: %w", err)
	}
	return taskgraph.FormatRunList(rows), nil
}

// execute builds the engine for one run and drives it to completion,
// holding the single-active-run invariant and the cost-recorder wiring.
func (a *taskGraphAdapter) execute(ctx context.Context, g *taskgraph.Graph, store *taskgraph.RunStore, onOutput func(string)) (string, error) {
	disp, err := a.dispatcher()
	if err != nil {
		return "", err
	}
	workspace, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("@taskgraph: resolve workspace: %w", err)
	}
	cfg := taskgraph.Config{
		Dispatcher:  disp,
		Gate:        &coderGateRunner{workspace: workspace},
		MaxParallel: taskGraphMaxParallel(),
		Workspace:   workspace,
		Checkpoint: func(label string) {
			if err := coderengine.CheckpointWorkspace(workspace, label); err != nil {
				a.logger.Debug("taskgraph checkpoint failed", zap.Error(err))
			}
		},
		CostPerCall: func(provider, model string, usage *models.UsageInfo) float64 {
			return estimateTurnCostUSD(provider, model, usage)
		},
		Logger: a.logger,
	}
	if onOutput != nil {
		cfg.OnEvent = func(ev taskgraph.Event) { onOutput(taskgraph.FormatEvent(ev) + "\n") }
	}

	engine, err := taskgraph.NewEngine(g, store, cfg)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	if a.active != nil {
		a.mu.Unlock()
		return "", errors.New("@taskgraph: a run is already active in this session (cancel it or wait)")
	}
	a.active = engine
	a.mu.Unlock()
	disp.SetCallUsageRecorder(engine.RecordCallUsage)
	defer func() {
		disp.SetCallUsageRecorder(nil)
		a.mu.Lock()
		a.active = nil
		a.mu.Unlock()
	}()

	return engine.Run(ctx)
}

// loadRun opens runID, or the most recent run when empty.
func (a *taskGraphAdapter) loadRun(base, runID string) (*taskgraph.RunStore, *taskgraph.Graph, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		rows, err := taskgraph.ListRuns(base)
		if err != nil || len(rows) == 0 {
			return nil, nil, errors.New("@taskgraph: no runs yet (create one with plan or run)")
		}
		runID = rows[0].RunID
	}
	store, err := taskgraph.OpenRun(base, runID)
	if err != nil {
		return nil, nil, fmt.Errorf("@taskgraph: %w", err)
	}
	g, err := store.LoadState()
	if err != nil {
		return nil, nil, fmt.Errorf("@taskgraph: %w", err)
	}
	return store, g, nil
}

// taskGraphMaxParallel resolves the session cap from the squad's existing
// worker-parallelism env.
func taskGraphMaxParallel() int {
	if v := strings.TrimSpace(os.Getenv(agentMaxWorkersEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return taskgraph.DefaultMaxParallel
}

// coderGateRunner runs validation commands through the coder engine so gates
// inherit the sandbox, the unsafe-command denylist and path validation.
type coderGateRunner struct {
	workspace string
}

// RunGateStep implements taskgraph.GateRunner.
func (r *coderGateRunner) RunGateStep(ctx context.Context, dir, cmd string, onLine func(string)) (string, error) {
	var buf strings.Builder
	var mu sync.Mutex
	sink := coderengine.NewStreamWriter(func(line string) {
		mu.Lock()
		buf.WriteString(line)
		buf.WriteString("\n")
		mu.Unlock()
		if onLine != nil {
			onLine(line)
		}
	})
	eng := coderengine.NewEngine(sink, sink, dir)
	err := eng.Execute(ctx, "exec", []string{
		"--cmd", cmd,
		"--timeout", strconv.Itoa(gateStepTimeoutSeconds),
	})
	sink.Flush()
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	return out, err
}
