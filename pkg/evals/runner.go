/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/coder/engine"
)

// DefaultConcurrency is how many trials run at once unless told otherwise:
// low enough to stay under common provider rate limits.
const DefaultConcurrency = 2

// maxFinalInReport caps the answer text stored per trial in the report.
const maxFinalInReport = 4000

// defaultCoderPolicy is installed in a coder case's sandbox when the case
// declares none: the coder may use every @coder subcommand inside its
// throwaway workspace. Each subcommand is listed by its exact pattern because
// policy precedence goes to the most specific pattern: a user's global
// "@coder patch: ask" outranks a local "@coder: allow", while a local rule
// with the SAME pattern replaces the global one when the files merge. The
// list comes from the engine's own schema, so a new subcommand is covered
// without touching the harness. Deny rules still win, safety-immune
// operations still ask, and with no human to answer they are denied.
func defaultCoderPolicy() []PolicyRule {
	rules := []PolicyRule{{Pattern: "@coder", Action: "allow"}}
	var schema struct {
		Subcommands []struct {
			Name string `json:"name"`
		} `json:"subcommands"`
	}
	if err := json.Unmarshal([]byte(engine.GetSchema()), &schema); err == nil {
		for _, sc := range schema.Subcommands {
			if sc.Name != "" {
				rules = append(rules, PolicyRule{Pattern: "@coder " + sc.Name, Action: "allow"})
			}
		}
	}
	return rules
}

// Options configures a run.
type Options struct {
	Executor    Executor
	Judge       Judge
	Concurrency int
	// Trials, when > 0, overrides every case's trial count.
	Trials int
	// Timeout, when > 0, overrides every case's timeout.
	Timeout time.Duration
	// MaxCostUSD stops scheduling new trials once candidate + judge spend
	// reaches it (0 = no cap). Trials already running finish.
	MaxCostUSD float64
	// Keep leaves sandboxes on disk for inspection.
	Keep bool
	// Progress, when set, is called after each trial completes.
	Progress func(TrialEvent)
}

// TrialEvent reports one finished trial.
type TrialEvent struct {
	Suite  string
	CaseID string
	Trial  TrialResult
	Total  int // trials in the run
	Done   int // trials finished so far
}

// TrialResult is one trial of one case.
type TrialResult struct {
	N            int           `json:"n"`
	Status       string        `json:"status"`
	Score        float64       `json:"score"`
	DurationMS   int64         `json:"duration_ms"`
	CostUSD      float64       `json:"cost_usd"`
	JudgeCostUSD float64       `json:"judge_cost_usd,omitempty"`
	Turns        int           `json:"turns,omitempty"`
	InputTokens  int64         `json:"input_tokens,omitempty"`
	OutputTokens int64         `json:"output_tokens,omitempty"`
	Provider     string        `json:"provider,omitempty"`
	Model        string        `json:"model,omitempty"`
	ToolCalls    []string      `json:"tool_calls,omitempty"`
	Final        string        `json:"final,omitempty"`
	Error        string        `json:"error,omitempty"`
	Sandbox      string        `json:"sandbox,omitempty"`
	Checks       []CheckResult `json:"checks,omitempty"`
}

type job struct {
	suite *Suite
	c     *Case
	n     int
}

// Run executes every case of every suite and returns the report. It never
// fails as a whole: per-trial problems are recorded as errors in the report.
func Run(ctx context.Context, suites []*Suite, opts Options) *Report {
	if opts.Concurrency < 1 {
		opts.Concurrency = DefaultConcurrency
	}
	rep := &Report{Schema: ReportSchema, StartedAt: time.Now().UTC()}
	for _, s := range suites {
		rep.Suites = append(rep.Suites, s.Name)
	}

	// One slot per trial, filled by the workers; cases keep suite order.
	type caseSlot struct {
		suite  *Suite
		c      *Case
		trials []TrialResult
	}
	var slots []*caseSlot
	slotOf := map[*Case]*caseSlot{}
	var jobs []job
	for _, s := range suites {
		for _, c := range s.Cases {
			cs := &caseSlot{suite: s, c: c}
			slots = append(slots, cs)
			slotOf[c] = cs
			if c.Skip != "" {
				continue
			}
			trials := c.Trials
			if opts.Trials > 0 {
				trials = opts.Trials
			}
			cs.trials = make([]TrialResult, trials)
			for n := 1; n <= trials; n++ {
				jobs = append(jobs, job{suite: s, c: c, n: n})
			}
		}
	}

	var (
		mu      sync.Mutex
		spent   float64
		done    int
		stopped bool
	)
	queue := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < opts.Concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range queue {
				mu.Lock()
				overBudget := opts.MaxCostUSD > 0 && spent >= opts.MaxCostUSD
				if overBudget {
					stopped = true
				}
				mu.Unlock()

				var tr TrialResult
				switch {
				case ctx.Err() != nil:
					tr = TrialResult{N: j.n, Status: StatusSkipped, Error: i18n.T("evals.run.canceled")}
				case overBudget:
					tr = TrialResult{N: j.n, Status: StatusSkipped, Error: i18n.T("evals.run.budget", opts.MaxCostUSD)}
				default:
					tr = runTrial(ctx, j, opts)
				}

				mu.Lock()
				spent += tr.CostUSD + tr.JudgeCostUSD
				done++
				slotOf[j.c].trials[j.n-1] = tr
				ev := TrialEvent{Suite: j.suite.Name, CaseID: j.c.ID, Trial: tr, Total: len(jobs), Done: done}
				mu.Unlock()
				if opts.Progress != nil {
					opts.Progress(ev)
				}
			}
		}()
	}
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	wg.Wait()

	for _, cs := range slots {
		rep.Cases = append(rep.Cases, aggregateCase(cs.suite, cs.c, cs.trials))
	}
	rep.BudgetExceeded = stopped
	rep.FinishedAt = time.Now().UTC()
	rep.DurationMS = rep.FinishedAt.Sub(rep.StartedAt).Milliseconds()
	rep.summarize()
	return rep
}

// runTrial prepares a sandbox, runs the candidate and grades the outcome.
func runTrial(ctx context.Context, j job, opts Options) TrialResult {
	c := j.c
	tr := TrialResult{N: j.n}
	timeout := c.Timeout.D()
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}

	sandbox, err := prepareSandbox(ctx, c)
	if sandbox != "" {
		if opts.Keep {
			tr.Sandbox = sandbox
		} else {
			defer func() { _ = os.RemoveAll(sandbox) }()
		}
	}
	if err != nil {
		tr.Status = StatusError
		tr.Error = err.Error()
		return tr
	}

	ex, runErr := opts.Executor.Execute(ctx, Request{
		Mode:    c.Mode,
		Prompt:  c.Prompt,
		Workdir: sandbox,
		Env:     c.Env,
		Timeout: timeout,
	})
	if ex != nil {
		tr.DurationMS = ex.Duration.Milliseconds()
		tr.Final = truncate(ex.Final, maxFinalInReport)
		if r := ex.Record; r != nil {
			tr.CostUSD = r.CostUSD
			tr.Turns = r.Turns
			tr.InputTokens = r.InputTokens
			tr.OutputTokens = r.OutputTokens
			tr.Provider = r.Provider
			tr.Model = r.Model
			tr.ToolCalls = r.ToolNames()
		}
	}
	if runErr != nil {
		tr.Status = StatusError
		tr.Error = runErr.Error()
		return tr
	}

	out := &Outcome{
		Prompt:   c.Prompt,
		Final:    ex.Final,
		Record:   ex.Record,
		Duration: ex.Duration,
		Sandbox:  sandbox,
		Env:      c.Env,
	}
	allPass := true
	var wsum, ssum float64
	for i := range c.Checks {
		cr := evaluate(ctx, &c.Checks[i], out, opts.Judge)
		tr.Checks = append(tr.Checks, cr)
		tr.JudgeCostUSD += cr.CostUSD
		wsum += cr.Weight
		ssum += cr.Weight * cr.Score
		if !cr.Passed {
			allPass = false
		}
	}
	if wsum > 0 {
		tr.Score = ssum / wsum
	}
	tr.Status = StatusFail
	if allPass {
		tr.Status = StatusPass
	}
	return tr
}

// prepareSandbox builds the case's throwaway workspace: fixture copy, inline
// files, coder policy and setup commands. It returns the directory even on
// error so the caller can clean it up.
func prepareSandbox(ctx context.Context, c *Case) (string, error) {
	dir, err := os.MkdirTemp("", "chatcli-eval-"+c.ID+"-*")
	if err != nil {
		return "", err
	}
	// Resolve symlinks (macOS /var → /private/var) so paths the candidate
	// reports match the ones checks use.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if c.FixtureDir != "" {
		if err := copyTree(c.FixtureDir, dir); err != nil {
			return dir, errors.New(i18n.T("evals.sandbox.fixture", c.Fixture, err))
		}
	}
	names := make([]string, 0, len(c.Files))
	for name := range c.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p, err := SandboxPath(dir, name)
		if err != nil {
			return dir, err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return dir, err
		}
		if err := os.WriteFile(p, []byte(c.Files[name]), 0o600); err != nil {
			return dir, err
		}
	}
	if c.Mode == ModeCoder {
		rules := c.Policy
		if len(rules) == 0 {
			rules = defaultCoderPolicy()
		}
		data, err := json.MarshalIndent(map[string]any{"merge": true, "rules": rules}, "", "  ")
		if err != nil {
			return dir, err
		}
		if err := os.WriteFile(filepath.Join(dir, "coder_policy.json"), data, 0o600); err != nil {
			return dir, err
		}
	}
	for _, line := range c.Setup {
		code, output, err := RunShell(ctx, line, dir, c.Env, DefaultCommandTimeout)
		if err != nil {
			return dir, errors.New(i18n.T("evals.sandbox.setup", line, err))
		}
		if code != 0 {
			return dir, errors.New(i18n.T("evals.sandbox.setup", line, fmt.Sprintf("exit %d: %s", code, tail(output, 400))))
		}
	}
	return dir, nil
}

// copyTree copies a fixture directory into dst, preserving file modes.
// Symlinks are recreated as links, never followed: a fixture must not pull
// files from outside itself into the sandbox.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil // sockets, devices: not fixture material
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src) //#nosec G304 -- walking the suite's own fixture tree
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm|0o600) //#nosec G304 -- destination inside the sandbox
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
