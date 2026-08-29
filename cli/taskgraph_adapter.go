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
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/taskgraph"
	"github.com/diillson/chatcli/cli/taskgraph/dash"
	"github.com/diillson/chatcli/models"
	coderengine "github.com/diillson/chatcli/pkg/coder/engine"
	"go.uber.org/zap"
	"golang.org/x/term"
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

	mu       sync.Mutex
	active   *taskgraph.Engine
	activeID string
	dashSrv  *dash.Server

	// pruneOnce runs the automatic retention sweep on first store access.
	pruneOnce sync.Once
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
	base, err := a.resolveBaseDir()
	if err != nil {
		return "", err
	}
	// Bounded store: runs are working state, not an archive. One sweep per
	// session removes anything older than the default retention.
	a.pruneOnce.Do(func() {
		if n, pruneErr := taskgraph.PruneRuns(base, taskgraph.DefaultRetention, a.activeRunID()); pruneErr != nil {
			a.logger.Warn("taskgraph auto-prune failed", zap.Error(pruneErr))
		} else if n > 0 {
			a.logger.Info("taskgraph auto-prune", zap.Int("removed", n))
		}
	})
	return base, nil
}

func (a *taskGraphAdapter) resolveBaseDir() (string, error) {
	if a.baseDirFn != nil {
		return a.baseDirFn()
	}
	return taskgraph.DefaultBaseDir()
}

// activeRunID names the in-flight run (never pruned), or "".
func (a *taskGraphAdapter) activeRunID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active == nil {
		return ""
	}
	return a.activeID
}

// Prune implements plugins.TaskGraphPruner: removes persisted runs older
// than the given retention ("30d", "72h", "all" = every run). The active
// run is never removed. Empty input uses the default retention.
func (a *taskGraphAdapter) Prune(olderThan string) (string, error) {
	base, err := a.baseDir()
	if err != nil {
		return "", err
	}
	retention, err := parseRetention(olderThan)
	if err != nil {
		return "", err
	}
	n, err := taskgraph.PruneRuns(base, retention, a.activeRunID())
	if err != nil {
		return "", fmt.Errorf("@taskgraph prune: %w", err)
	}
	scope := "older than " + retention.String()
	if retention <= 0 {
		scope = "all (active run kept)"
	}
	return fmt.Sprintf("pruned %d run(s), scope: %s", n, scope), nil
}

// parseRetention accepts "", "all", Go durations ("72h") and day suffixes
// ("30d") — lenient like every tool-arg parser in the house.
func parseRetention(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "":
		return taskgraph.DefaultRetention, nil
	case "all", "everything", "0":
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("@taskgraph prune: invalid retention %q (use \"30d\", \"72h\" or \"all\")", s)
	}
	return d, nil
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

// Dash implements plugins.TaskGraphDashboarder: serves the read-only live
// dashboard (one server per session, ephemeral 127.0.0.1 port) and opens the
// browser only on a real terminal — never from a daemon or pipe.
func (a *taskGraphAdapter) Dash(runID string) (string, error) {
	base, err := a.baseDir()
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	if a.dashSrv == nil {
		srv, startErr := dash.Start(base)
		if startErr != nil {
			a.mu.Unlock()
			return "", fmt.Errorf("@taskgraph dash: %w", startErr)
		}
		a.dashSrv = srv
	}
	srv := a.dashSrv
	a.mu.Unlock()

	dashURL := srv.URL()
	if runID = strings.TrimSpace(runID); runID != "" {
		dashURL += "?run=" + url.QueryEscape(runID)
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		_ = openBrowserURL(dashURL)
	}
	return "task graph dashboard: " + dashURL + " (read-only; closing it never affects the run)", nil
}

// shutdownDash stops the dashboard server, if one is up.
func (a *taskGraphAdapter) shutdownDash(ctx context.Context) {
	a.mu.Lock()
	srv := a.dashSrv
	a.dashSrv = nil
	a.mu.Unlock()
	if srv != nil {
		_ = srv.Shutdown(ctx)
	}
}

// openBrowserURL opens a URL in the OS default browser.
func openBrowserURL(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Start() //#nosec G204 -- local dashboard URL built by this process, not user/model input
	case "linux":
		return exec.Command("xdg-open", rawURL).Start() //#nosec G204 -- local dashboard URL built by this process, not user/model input
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start() //#nosec G204 -- local dashboard URL built by this process, not user/model input
	default:
		return fmt.Errorf("unsupported platform for browser open: %s", runtime.GOOS)
	}
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
		// No trailing newline: the stream renderer prints one line per
		// callback and an extra \n renders as a blank spacer line.
		cfg.OnEvent = func(ev taskgraph.Event) { onOutput(taskgraph.FormatEvent(ev)) }
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
	a.activeID = store.RunID()
	a.mu.Unlock()
	disp.SetCallUsageRecorder(engine.RecordCallUsage)
	defer func() {
		disp.SetCallUsageRecorder(nil)
		a.mu.Lock()
		a.active = nil
		a.activeID = ""
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
