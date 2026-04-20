/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Phase 2 (#2) — Plan-and-Solve / ReWOO executor.
 *
 * The runner takes a parsed Plan, dispatches each step in topological
 * order via the existing workers.Dispatcher (no new infra, no parallel
 * code path), and resolves #E<n> placeholders against the running
 * outputs map. The final report is a deterministic summary that the
 * caller can inject into chat history before the orchestrator takes
 * over with full context.
 */
package quality

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/workers"
	"go.uber.org/zap"
)

// StepDispatcher is the minimal slice of workers.Dispatcher the runner
// needs. Defined as an interface so tests can drive PlanRunner without
// spinning up a real LLMManager + registry.
type StepDispatcher interface {
	Dispatch(ctx context.Context, calls []workers.AgentCall) []workers.AgentResult
}

// PlanRunner executes a parsed Plan via a StepDispatcher.
type PlanRunner struct {
	dispatcher StepDispatcher
	logger     *zap.Logger
}

// NewPlanRunner returns a runner. nil logger upgrades to a no-op so the
// caller never has to nil-check.
func NewPlanRunner(d StepDispatcher, logger *zap.Logger) *PlanRunner {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &PlanRunner{dispatcher: d, logger: logger}
}

// PlanRunResult bundles the executor's final state for the caller.
//
// FinalReport is a multi-line, human-readable summary safe to inject
// into the chat history as a synthetic system message.
//
// StepResults preserves the per-step outputs so subsequent post-hooks
// (Reflexion, Verifier, …) can attribute discrepancies back to the
// specific worker that produced them.
type PlanRunResult struct {
	Plan          *Plan
	StepResults   map[string]workers.AgentResult
	StepOutputs   map[string]string
	FinalReport   string
	HadErrors     bool
	StepsExecuted int
}

// Execute runs the plan top-to-bottom in topological order, dispatching
// one step at a time so each output is available to resolve placeholders
// in subsequent steps.
//
// A failing step does not abort the run by default — downstream steps
// see "<error: …>" substituted for missing outputs. This mirrors how the
// orchestrator reacts to per-agent errors today (continue, summarize,
// surface to the model). The HadErrors flag flips to true so the caller
// can decide whether to escalate.
func (r *PlanRunner) Execute(ctx context.Context, plan *Plan) *PlanRunResult {
	if plan == nil {
		return &PlanRunResult{
			Plan:        plan,
			FinalReport: "no plan to execute",
		}
	}

	order := plan.TopologicalOrder()
	stepByID := make(map[string]*PlanStep, len(plan.Steps))
	for _, s := range plan.Steps {
		stepByID[s.ID] = s
	}

	outputs := make(map[string]string, len(plan.Steps))
	results := make(map[string]workers.AgentResult, len(plan.Steps))
	hadErrors := false
	executed := 0
	startedAt := time.Now()

	for _, id := range order {
		if err := ctx.Err(); err != nil {
			r.logger.Warn("plan execution aborted by context", zap.Error(err))
			break
		}
		step, ok := stepByID[id]
		if !ok {
			r.logger.Warn("plan refers to unknown step id; skipping", zap.String("id", id))
			continue
		}

		resolvedTask := ResolvePlaceholders(step.Task, outputs)
		call := step.ToAgentCall(resolvedTask)

		r.logger.Info("plan step dispatching",
			zap.String("id", id),
			zap.String("agent", string(call.Agent)),
			zap.String("task", truncate(resolvedTask, 120)))

		batch := r.dispatcher.Dispatch(ctx, []workers.AgentCall{call})
		if len(batch) == 0 {
			r.logger.Warn("plan dispatcher returned no result; skipping step", zap.String("id", id))
			outputs[id] = fmt.Sprintf("<error: dispatcher returned no result for %s>", id)
			hadErrors = true
			continue
		}
		res := batch[0]
		results[id] = res
		executed++

		if res.Error != nil {
			hadErrors = true
			outputs[id] = fmt.Sprintf("<error: %s>", res.Error.Error())
			r.logger.Warn("plan step failed",
				zap.String("id", id),
				zap.String("agent", string(call.Agent)),
				zap.Error(res.Error))
			continue
		}
		outputs[id] = strings.TrimSpace(res.Output)
	}

	report := buildPlanReport(plan, order, results, time.Since(startedAt))
	return &PlanRunResult{
		Plan:          plan,
		StepResults:   results,
		StepOutputs:   outputs,
		FinalReport:   report,
		HadErrors:     hadErrors,
		StepsExecuted: executed,
	}
}

// buildPlanReport renders a deterministic, human-friendly summary of
// the run. The format mirrors workers.FormatResults so the orchestrator
// is already trained to consume it. Each step gets its id, agent, status
// and a head-truncated output excerpt to keep the report bounded.
func buildPlanReport(plan *Plan, order []string, results map[string]workers.AgentResult, dur time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- Plan Execution Report (Plan-and-Solve / ReWOO, %s) ---\n", dur.Round(time.Millisecond))
	if plan.TaskSummary != "" {
		fmt.Fprintf(&b, "Task summary: %s\n", plan.TaskSummary)
	}
	for _, id := range order {
		res, ok := results[id]
		if !ok {
			fmt.Fprintf(&b, "\n[%s] SKIPPED (not executed)\n", id)
			continue
		}
		status := "OK"
		if res.Error != nil {
			status = fmt.Sprintf("FAILED — %v", res.Error)
		}
		fmt.Fprintf(&b, "\n[%s] agent=%s status=%s duration=%s\n",
			id, res.Agent, status, res.Duration.Round(time.Millisecond))
		fmt.Fprintf(&b, "  Task: %s\n", truncate(res.Task, 200))
		if res.Output != "" {
			fmt.Fprintf(&b, "  Output: %s\n", truncate(strings.TrimSpace(res.Output), 400))
		}
	}
	return b.String()
}

// RunFromPlannerOutput is the convenience entry point: parses the
// planner's raw text, validates, and executes. Returns the run result
// plus a parse error (if any). When parsing fails the runner is not
// invoked — callers should fall back to non-plan-first dispatch.
func (r *PlanRunner) RunFromPlannerOutput(ctx context.Context, raw string) (*PlanRunResult, error) {
	plan, err := ParsePlan(raw)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, errors.New("ParsePlan returned nil plan without error")
	}
	return r.Execute(ctx, plan), nil
}
