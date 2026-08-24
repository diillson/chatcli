/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"errors"
	"fmt"

	"github.com/diillson/chatcli/i18n"
)

// maybeAnnounceBudget prints the one-shot budget notice when the session
// crossed a budget level since the last check (warning threshold, or the
// limit itself). Called right after usage is recorded so the user learns
// about the crossing on the turn it happens — not only when they remember
// to run /cost.
func (cli *ChatCLI) maybeAnnounceBudget() {
	if cli.costTracker == nil {
		return
	}
	level, msg, ok := cli.costTracker.TakeBudgetTransition()
	if !ok || msg == "" {
		return
	}
	color := ColorYellow
	if level == BudgetExceeded {
		color = ColorRed
	}
	fmt.Println(colorize("  "+msg, color))
}

// budgetBlockedErr returns a localized error when the hard-stop gate
// (CHATCLI_BUDGET_HARD_STOP) refuses new LLM turns because the session
// budget is exhausted. Nil when turns may proceed.
func (cli *ChatCLI) budgetBlockedErr() error {
	if cli.costTracker != nil && cli.costTracker.BudgetBlocked() {
		return errors.New(i18n.T("cost.budget.blocked"))
	}
	return nil
}
