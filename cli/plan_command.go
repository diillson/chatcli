/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /plan — force Plan-First (Plan-and-Solve / ReWOO) for the next agent run.
 *
 * Two shapes:
 *   /plan                → arms the one-shot flag; user then runs /agent <task>
 *                          or /coder <task> to consume it
 *   /plan <task...>      → equivalent to "arm + run /agent <task>" so the
 *                          user gets a single-line workflow
 *
 * The actual planning + execution happens inside AgentMode.Run via
 * runPlanFirstIfApplicable; this command only flips the trigger.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// handleThinkingCommand's sibling for Plan-First. Returns true when the
// caller should immediately enter agent mode (because /plan was given a
// task) so command_handler can panic with agentModeRequest.
func (cli *ChatCLI) handlePlanCommand(userInput string) bool {
	cli.pendingPlanFirst = true

	rest := strings.TrimSpace(strings.TrimPrefix(userInput, "/plan"))
	if rest == "" {
		fmt.Println(colorize("  "+i18n.T("plan.armed"), ColorGreen))
		return false
	}
	// Inline form: /plan <task> → arm and immediately ask the agent
	// loop to consume it. We mirror /agent's behaviour: pendingAction
	// is set and the caller panics with agentModeRequest.
	cli.pendingAction = "agent"
	return true
}
