/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * eval.go
 *
 * `chatcli eval` — the offline evaluation harness (pkg/evals) from the
 * command line, for CI and for comparing models:
 *
 *	chatcli eval run <suite.yaml|dir> [flags]   run, grade, report, gate
 *	chatcli eval list <suite.yaml|dir>          the cases a run would execute
 *	chatcli eval validate <suite.yaml|dir>      parse and validate only
 *	chatcli eval compare <baseline.json> <current.json>
 *
 * Only `run` boots an LLM, and only for the judge: the candidate runs as a
 * subprocess of this very binary.
 */
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/pkg/evals"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
)

// Exit codes of `chatcli eval`, a CI contract.
const (
	EvalExitOK         = 0
	EvalExitFailed     = 1 // pass rate below --min-pass-rate
	EvalExitUsage      = 2 // bad arguments or an invalid suite
	EvalExitRegression = 3 // worse than --baseline
)

// DefaultEvalPath is the suite location used when none is given.
const DefaultEvalPath = "evals"

// EvalNeedsLLM reports whether the eval subcommand in args boots an LLM
// manager (only `run` does, for the judge).
func EvalNeedsLLM(args []string) bool {
	return len(args) > 0 && args[0] == "run"
}

// RunEval dispatches `chatcli eval`. mgr may be nil for subcommands that do
// not need it. It returns the process exit code.
func RunEval(ctx context.Context, args []string, mgr manager.LLMManager, logger *zap.Logger, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, i18n.T("evals.cmd.usage"))
		return EvalExitUsage
	}
	switch args[0] {
	case "run":
		return evalRun(ctx, args[1:], mgr, logger, stdout, stderr)
	case "list", "ls":
		return evalList(args[1:], stdout, stderr, false)
	case "validate":
		return evalList(args[1:], stdout, stderr, true)
	case "compare", "diff":
		return evalCompare(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, i18n.T("evals.cmd.usage"))
		return EvalExitOK
	}
	fmt.Fprintln(stderr, i18n.T("evals.cmd.unknown", args[0]))
	fmt.Fprintln(stderr, i18n.T("evals.cmd.usage"))
	return EvalExitUsage
}

// evalRunFlags holds `eval run` options.
type evalRunFlags struct {
	provider, model           string
	judgeProvider, judgeModel string
	concurrency, trials       int
	timeout                   time.Duration
	filter                    string
	out, markdown             string
	baseline                  string
	maxRegressions            int
	tolerance                 float64
	minPassRate               float64
	maxCost                   float64
	keep, withMemory, asJSON  bool
	quiet                     bool
	bin                       string
}

func newEvalRunFlagSet(f *evalRunFlags, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("eval run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.provider, "provider", "", i18n.T("evals.flag.provider"))
	fs.StringVar(&f.model, "model", "", i18n.T("evals.flag.model"))
	fs.StringVar(&f.judgeProvider, "judge-provider", "", i18n.T("evals.flag.judge_provider"))
	fs.StringVar(&f.judgeModel, "judge-model", "", i18n.T("evals.flag.judge_model"))
	fs.IntVar(&f.concurrency, "concurrency", evals.DefaultConcurrency, i18n.T("evals.flag.concurrency"))
	fs.IntVar(&f.trials, "trials", 0, i18n.T("evals.flag.trials"))
	fs.DurationVar(&f.timeout, "timeout", 0, i18n.T("evals.flag.timeout"))
	fs.StringVar(&f.filter, "filter", "", i18n.T("evals.flag.filter"))
	fs.StringVar(&f.out, "out", "", i18n.T("evals.flag.out"))
	fs.StringVar(&f.markdown, "markdown", "", i18n.T("evals.flag.markdown"))
	fs.StringVar(&f.baseline, "baseline", "", i18n.T("evals.flag.baseline"))
	fs.IntVar(&f.maxRegressions, "max-regressions", 0, i18n.T("evals.flag.max_regressions"))
	fs.Float64Var(&f.tolerance, "tolerance", 0, i18n.T("evals.flag.tolerance"))
	fs.Float64Var(&f.minPassRate, "min-pass-rate", 1, i18n.T("evals.flag.min_pass_rate"))
	fs.Float64Var(&f.maxCost, "max-cost", 0, i18n.T("evals.flag.max_cost"))
	fs.BoolVar(&f.keep, "keep", false, i18n.T("evals.flag.keep"))
	fs.BoolVar(&f.withMemory, "with-memory", false, i18n.T("evals.flag.with_memory"))
	fs.BoolVar(&f.asJSON, "json", false, i18n.T("evals.flag.json"))
	fs.BoolVar(&f.quiet, "quiet", false, i18n.T("evals.flag.quiet"))
	fs.StringVar(&f.bin, "bin", "", i18n.T("evals.flag.bin"))
	return fs
}

// parseInterleaved parses flags that may appear before or after the
// positional arguments (Go's flag package stops at the first positional).
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func (f *evalRunFlags) validate() error {
	switch {
	case f.concurrency < 1:
		return errors.New(i18n.T("evals.cmd.bad_flag", "--concurrency"))
	case f.trials < 0 || f.trials > evals.MaxTrials:
		return errors.New(i18n.T("evals.cmd.bad_flag", "--trials"))
	case f.timeout < 0:
		return errors.New(i18n.T("evals.cmd.bad_flag", "--timeout"))
	case f.maxRegressions < 0:
		return errors.New(i18n.T("evals.cmd.bad_flag", "--max-regressions"))
	case f.tolerance < 0 || f.tolerance > 1:
		return errors.New(i18n.T("evals.cmd.bad_flag", "--tolerance"))
	case f.minPassRate < 0 || f.minPassRate > 1:
		return errors.New(i18n.T("evals.cmd.bad_flag", "--min-pass-rate"))
	case f.maxCost < 0:
		return errors.New(i18n.T("evals.cmd.bad_flag", "--max-cost"))
	}
	return nil
}

// loadEvalSuites resolves the path argument (default ./evals), loads and
// filters the suites.
func loadEvalSuites(positional []string, filter string) ([]*evals.Suite, error) {
	path := DefaultEvalPath
	switch len(positional) {
	case 0:
	case 1:
		path = positional[0]
	default:
		return nil, errors.New(i18n.T("evals.cmd.one_path"))
	}
	suites, err := evals.Load(path)
	if err != nil {
		return nil, err
	}
	suites = evals.Filter(suites, strings.Split(filter, ","))
	if len(suites) == 0 {
		return nil, errors.New(i18n.T("evals.cmd.no_match", filter))
	}
	return suites, nil
}

func evalList(args []string, stdout, stderr io.Writer, validateOnly bool) int {
	fs := flag.NewFlagSet("eval list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	filter := fs.String("filter", "", i18n.T("evals.flag.filter"))
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return EvalExitUsage
	}
	suites, err := loadEvalSuites(positional, *filter)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return EvalExitUsage
	}
	if validateOnly {
		cases := 0
		for _, s := range suites {
			cases += len(s.Cases)
		}
		fmt.Fprintln(stdout, i18n.T("evals.cmd.valid", len(suites), cases))
		return EvalExitOK
	}
	evals.WriteCaseList(stdout, suites)
	return EvalExitOK
}

func evalCompare(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxRegressions := fs.Int("max-regressions", 0, i18n.T("evals.flag.max_regressions"))
	tolerance := fs.Float64("tolerance", 0, i18n.T("evals.flag.tolerance"))
	asJSON := fs.Bool("json", false, i18n.T("evals.flag.json"))
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return EvalExitUsage
	}
	if len(positional) != 2 {
		fmt.Fprintln(stderr, i18n.T("evals.cmd.usage"))
		return EvalExitUsage
	}
	base, err := evals.ReadReport(positional[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return EvalExitUsage
	}
	cur, err := evals.ReadReport(positional[1])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return EvalExitUsage
	}
	cmp := evals.Compare(base, cur)
	if *asJSON {
		writeJSON(stdout, cmp)
	} else {
		evals.WriteComparison(stdout, cmp)
	}
	if cmp.Regressed(*maxRegressions, *tolerance) {
		return EvalExitRegression
	}
	return EvalExitOK
}

func evalRun(ctx context.Context, args []string, mgr manager.LLMManager, logger *zap.Logger, stdout, stderr io.Writer) int {
	var f evalRunFlags
	fs := newEvalRunFlagSet(&f, stderr)
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return EvalExitUsage
	}
	if err := f.validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return EvalExitUsage
	}
	suites, err := loadEvalSuites(positional, f.filter)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return EvalExitUsage
	}
	var baseline *evals.Report
	if f.baseline != "" {
		if baseline, err = evals.ReadReport(f.baseline); err != nil {
			fmt.Fprintln(stderr, err)
			return EvalExitUsage
		}
	}

	bin := f.bin
	if bin == "" {
		if bin, err = os.Executable(); err != nil {
			fmt.Fprintln(stderr, i18n.T("evals.cmd.no_binary", err))
			return EvalExitUsage
		}
	}
	exec := &evals.BinaryExecutor{
		Bin:        bin,
		Provider:   f.provider,
		Model:      f.model,
		WithMemory: f.withMemory,
	}
	if res := config.ActiveDotenv(); res.Exists {
		exec.Dotenv = res.Path
	}

	opts := evals.Options{
		Executor:    exec,
		Concurrency: f.concurrency,
		Trials:      f.trials,
		Timeout:     f.timeout,
		MaxCostUSD:  f.maxCost,
		Keep:        f.keep,
	}
	if !f.quiet {
		opts.Progress = func(ev evals.TrialEvent) { fmt.Fprintln(stderr, evals.ProgressLine(ev)) }
	}

	var judge *cli.EvalJudge
	var session *cli.ChatCLI
	if evals.HasJudge(suites) {
		if mgr == nil {
			fmt.Fprintln(stderr, i18n.T("evals.judge.unavailable"))
			return EvalExitUsage
		}
		session, err = cli.NewChatCLI(ctx, mgr, logger)
		if err != nil {
			fmt.Fprintln(stderr, i18n.T("evals.cmd.judge_boot", err))
			return EvalExitUsage
		}
		session.SetAuditSurface("eval")
		session.SetUnattended(true)
		defer session.FinalizeSpend(context.WithoutCancel(ctx))
		jp, jm := f.judgeProvider, f.judgeModel
		if jp == "" && jm == "" {
			jp, jm = suiteJudge(suites)
		}
		if judge, err = session.NewEvalJudge(jp, jm); err != nil {
			fmt.Fprintln(stderr, i18n.T("evals.cmd.judge_boot", err))
			return EvalExitUsage
		}
		opts.Judge = &evals.LLMJudge{Send: judge.Send}
	}

	cases := 0
	for _, s := range suites {
		cases += len(s.Cases)
	}
	if !f.quiet {
		fmt.Fprintln(stderr, i18n.T("evals.cmd.starting", cases, len(suites), f.concurrency))
		if !f.withMemory {
			fmt.Fprintln(stderr, i18n.T("evals.cmd.hermetic"))
		}
	}

	rep := evals.Run(ctx, suites, opts)
	rep.Version = version.GetCurrentVersion().Version
	rep.WithMemory = f.withMemory
	if judge != nil {
		rep.Judge = &evals.JudgeTarget{Provider: judge.Provider(), Model: judge.Model()}
		rep.SelfJudged = sameTarget(rep.Candidate, *rep.Judge)
	}

	var cmp *evals.Comparison
	if baseline != nil {
		cmp = evals.Compare(baseline, rep)
	}

	if f.out != "" {
		if err := evals.WriteReport(f.out, rep); err != nil {
			fmt.Fprintln(stderr, i18n.T("evals.cmd.write_failed", f.out, err))
		}
	}
	if f.markdown != "" {
		if err := os.WriteFile(f.markdown, []byte(evals.Markdown(rep, cmp)), 0o600); err != nil {
			fmt.Fprintln(stderr, i18n.T("evals.cmd.write_failed", f.markdown, err))
		}
	}
	if f.asJSON {
		writeJSON(stdout, rep)
	} else {
		evals.WriteSummary(stdout, rep)
		if cmp != nil {
			evals.WriteComparison(stdout, cmp)
		}
	}

	if cmp != nil && cmp.Regressed(f.maxRegressions, f.tolerance) {
		return EvalExitRegression
	}
	if !rep.MeetsPassRate(f.minPassRate) {
		return EvalExitFailed
	}
	return EvalExitOK
}

// suiteJudge returns the first judge a loaded suite declares.
func suiteJudge(suites []*evals.Suite) (string, string) {
	for _, s := range suites {
		if s.Judge.Provider != "" || s.Judge.Model != "" {
			return s.Judge.Provider, s.Judge.Model
		}
	}
	return "", ""
}

// sameTarget reports whether the judge graded its own model's answers.
func sameTarget(candidate, judge evals.JudgeTarget) bool {
	if candidate.Model == "" || judge.Model == "" {
		return false
	}
	return strings.EqualFold(candidate.Model, judge.Model) &&
		(candidate.Provider == "" || judge.Provider == "" || strings.EqualFold(candidate.Provider, judge.Provider))
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
