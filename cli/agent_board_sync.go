/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * agent_board_sync — deterministic turn-boundary reconciliation between the
 * squad board and the run registry.
 *
 * The orchestrator is instructed to move cards as work progresses, but an
 * LLM under a long ReAct loop reliably forgets: cards sit in "doing" long
 * after their linked runs finished. This helper detects exactly that state
 * mechanically and injects a compact [BOARD SYNC] block at the turn
 * boundary, so the model is reminded with REAL card IDs (never guessed)
 * in the same turn it can act on them.
 */
package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/board"
	"github.com/diillson/chatcli/models"
)

// buildBoardSyncNotice returns a [BOARD SYNC] block listing cards in
// "doing" whose linked runs ALL reached a terminal state — the mechanical
// signal that the orchestrator owes the board a move/note. Returns "" when
// there is nothing to reconcile or the same notice was already injected
// (the model gets one nudge per distinct stale state, not one per turn).
func (a *AgentMode) buildBoardSyncNotice() string {
	if !a.parallelMode {
		return ""
	}
	store := squadBoard()
	if store == nil {
		return ""
	}
	cards, err := store.List(board.ColDoing)
	if err != nil || len(cards) == 0 {
		a.lastBoardNudge = ""
		return ""
	}

	reg := agentRunsRegistry()
	var stale []board.Card
	for _, c := range cards {
		if len(c.RunIDs) == 0 {
			continue
		}
		allDone := true
		for _, id := range c.RunIDs {
			info, ok := reg.Get(id)
			if !ok || !info.Status.Terminal() {
				// Unknown runs (evicted history, other process) are treated
				// as possibly alive — never nudge on incomplete evidence.
				allDone = false
				break
			}
		}
		if allDone {
			stale = append(stale, c)
		}
	}
	if len(stale) == 0 {
		a.lastBoardNudge = ""
		return ""
	}

	var b strings.Builder
	b.WriteString("[BOARD SYNC] Cards still in doing although ALL their linked runs finished:\n")
	for _, c := range stale {
		fmt.Fprintf(&b, "- %s: %s (runs: %s)\n", c.ID, c.Title, strings.Join(c.RunIDs, ", "))
	}
	b.WriteString("Reconcile the board NOW, in this turn: @board move <id> review (then dispatch the reviewer) " +
		"or @board note the outcome and re-dispatch if the work failed. Use the card IDs above verbatim.")

	notice := b.String()
	if notice == a.lastBoardNudge {
		return ""
	}
	a.lastBoardNudge = notice
	return notice
}

// injectBoardSyncNotice appends the reconciliation block (when any) to the
// conversation as a user-role message. Called at the orchestrator turn
// boundary alongside the mail drain.
func (a *AgentMode) injectBoardSyncNotice() {
	if notice := a.buildBoardSyncNotice(); notice != "" {
		a.cli.history = append(a.cli.history, models.Message{Role: "user", Content: notice})
	}
}
