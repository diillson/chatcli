/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Phase 2 (#2) — Plan-and-Solve / ReWOO trigger for /agent and /coder.
 *
 * Lives outside agent_mode.go (already 3800+ lines) so the wiring is
 * easy to audit. The trigger is invoked from AgentMode.Run after the
 * user's query is appended to history but before the ReAct loop
 * starts. When it fires, it adds two synthetic messages to history:
 *   1. an assistant message containing the structured plan (so the
 *      orchestrator sees what was attempted), and
 *   2. a system message containing the deterministic execution
 *      report so the orchestrator can finalize with the gathered
 *      outputs.
 */
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// runPlanFirstIfApplicable checks the quality config and the one-shot
// /plan flag, then optionally runs a structured Plan-and-Solve cycle.
//
// All exits are silent (the only side effect is appending to history)
// because Plan-First is meant to be a behind-the-scenes accelerator,
// not a UI feature. /config quality + the deterministic report in
// history are the visible artifacts.
func (a *AgentMode) runPlanFirstIfApplicable(ctx context.Context, userQuery string) {
	if a.agentDispatcher == nil || a.agentRegistry == nil {
		return
	}

	// One-shot trigger from /plan beats the config; clear it after read
	// so a subsequent /agent invocation behaves normally.
	forced := a.cli.pendingPlanFirst
	a.cli.pendingPlanFirst = false

	if !forced && !quality.ShouldPlanFirst(a.qualityConfig.PlanFirst, userQuery) {
		return
	}

	planner, ok := a.agentRegistry.Get(workers.AgentTypePlanner)
	if !ok {
		a.logger.Warn("Plan-First skipped: planner agent not registered")
		return
	}
	_ = planner // signature contract; dispatcher resolves the agent again

	a.logger.Info("Plan-First triggered",
		zap.Bool("forced", forced),
		zap.String("mode", a.qualityConfig.PlanFirst.Mode),
		zap.Int("complexity", quality.ComplexityScore(userQuery)))

	// Step 1: ask the planner for a structured JSON plan via the
	// dispatcher so model routing, effort hints, policy and reasoning
	// auto-enable all fire correctly.
	plannerCall := workers.AgentCall{
		Agent: workers.AgentTypePlanner,
		Task:  workers.PlannerStructuredOutputDirective + "\n" + userQuery,
		ID:    "plan-first",
	}
	planResults := a.agentDispatcher.Dispatch(ctx, []workers.AgentCall{plannerCall})
	if len(planResults) == 0 || planResults[0].Error != nil {
		var errMsg string
		if len(planResults) > 0 {
			errMsg = planResults[0].Error.Error()
		}
		a.logger.Warn("Plan-First aborted: planner call failed",
			zap.String("error", errMsg))
		return
	}

	// Step 2: parse + execute via PlanRunner. The runner reuses the
	// same dispatcher, so quality hooks (Refine, Verify, …) keep
	// firing per step.
	runner := quality.NewPlanRunner(a.agentDispatcher, a.logger)
	runRes, parseErr := runner.RunFromPlannerOutput(ctx, planResults[0].Output)
	if parseErr != nil {
		a.logger.Warn("Plan-First aborted: plan parse failed",
			zap.String("error", parseErr.Error()),
			zap.String("planner_output_preview", truncatePlannerOutput(planResults[0].Output, 240)))
		return
	}
	if runRes == nil {
		return
	}

	// Step 3: surface the result to the user (compact one-liner) and
	// inject context into history for the orchestrator. Two messages:
	//   - assistant: shows the model what was already attempted
	//   - system:    feeds the deterministic per-step results
	header := i18n.T("plan_first.executed", runRes.StepsExecuted)
	if runRes.HadErrors {
		header += " " + i18n.T("plan_first.with_errors")
	}
	fmt.Println(colorize("  "+header, ColorCyan))

	planJSON := strings.TrimSpace(planResults[0].Output)
	if planJSON != "" {
		a.cli.history = append(a.cli.history, models.Message{
			Role:    "assistant",
			Content: i18n.T("plan_first.synth_plan_header") + "\n\n" + planJSON,
		})
	}
	a.cli.history = append(a.cli.history, models.Message{
		Role:    "system",
		Content: runRes.FinalReport + "\n\n" + i18n.T("plan_first.orchestrator_handoff"),
	})
}

// truncatePlannerOutput keeps Plan-First diagnostics bounded so a
// runaway planner can't flood the logs.
func truncatePlannerOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
