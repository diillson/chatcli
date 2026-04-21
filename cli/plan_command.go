/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /plan — force Plan-First (Plan-and-Solve / ReWOO) for the next agent run.
 *
 * Shapes:
 *   /plan                            → arms the one-shot flag; user then runs /agent <task>
 *                                      or /coder <task> to consume it
 *   /plan <task...>                  → arm + run /agent <task>  (default, matches doc)
 *   /plan agent <task...>            → explicit agent mode
 *   /plan coder <task...>            → arm + run /coder <task>  (coder with plan-first)
 *   /plan preview <task...>          → dry-run: generate and display the plan only
 *   /plan dry <task...>              → alias of preview
 *
 * The actual planning + execution happens inside AgentMode.Run via
 * runPlanFirstIfApplicable; this command only flips the trigger and
 * picks the consuming mode.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// planCommandRoute is what handlePlanCommand signals back to the caller.
type planCommandRoute int

const (
	planRouteNone  planCommandRoute = iota // no mode entry (bare /plan)
	planRouteAgent                         // enter agent mode
	planRouteCoder                         // enter coder mode
)

// handlePlanCommand parses /plan, arms pendingPlanFirst (+ optionally
// pendingPlanDryRun), and tells the caller which mode to enter (if any).
// Returning planRouteAgent or planRouteCoder means command_handler
// should panic with the matching sentinel error after this returns.
func (cli *ChatCLI) handlePlanCommand(userInput string) planCommandRoute {
	cli.pendingPlanFirst = true
	cli.pendingPlanDryRun = false

	rest := strings.TrimSpace(strings.TrimPrefix(userInput, "/plan"))
	if rest == "" {
		fmt.Println(colorize("  "+i18n.T("plan.armed"), ColorGreen))
		return planRouteNone
	}

	// /plan preview|dry <task> → generate plan, show it, don't execute.
	// Runs under agent mode so the dispatcher/registry are available, but
	// AgentMode.Run returns before the ReAct loop (see planDryRunHandled).
	if strings.HasPrefix(rest, "preview ") || rest == "preview" ||
		strings.HasPrefix(rest, "dry ") || rest == "dry" {
		var task string
		switch {
		case strings.HasPrefix(rest, "preview"):
			task = strings.TrimSpace(strings.TrimPrefix(rest, "preview"))
		default:
			task = strings.TrimSpace(strings.TrimPrefix(rest, "dry"))
		}
		if task == "" {
			// Without a task there's nothing to plan; keep the arm + hint.
			fmt.Println(colorize("  "+i18n.T("plan.preview_usage"), ColorYellow))
			cli.pendingPlanFirst = false
			return planRouteNone
		}
		cli.pendingPlanDryRun = true
		cli.pendingAction = "agent"
		return planRouteAgent
	}

	// /plan coder <task> → coder mode with plan-first armed
	if strings.HasPrefix(rest, "coder ") || rest == "coder" {
		task := strings.TrimSpace(strings.TrimPrefix(rest, "coder"))
		if task == "" {
			fmt.Println(colorize("  "+i18n.T("plan.armed"), ColorGreen))
			return planRouteNone
		}
		cli.pendingAction = "coder"
		return planRouteCoder
	}

	// /plan <task> (or /plan agent <task>) → default: agent mode
	cli.pendingAction = "agent"
	return planRouteAgent
}
