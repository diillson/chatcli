/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * agent_side_commands — lets the user drive the observation commands
 * (/agents, /board, /mail, /jobs) WHILE the agent/coder loop runs, instead
 * of the terminal being fully owned by the run until it finishes.
 *
 * The centralized stdin reader classifies these lines at the producer: they
 * never enter the stdinLines channel, so a security prompt cannot read
 * "/board" as an answer and the type-ahead drain cannot hand them to the
 * LLM as instructions. When a live display (turn spinner or the multi-agent
 * dispatch panel) is active, the command executes immediately — the display
 * pauses, the command output prints, the display resumes. Otherwise (the
 * terminal is owned by a security prompt, or the loop is between displays)
 * the command queues and runs at the next turn boundary, right before the
 * type-ahead drain.
 *
 * The allowlist is deliberately small and MUST NOT include mode-switch
 * commands (/agent, /coder, /run, /plan, /exit): those unwind the REPL via
 * panic sentinels and would tear the running loop down from a goroutine
 * that cannot recover them.
 */
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
)

// sideCommandRoots are the slash-command words runnable mid-run. Matching
// is exact-word or word+space (same shape as the command router's
// word-prefix routes).
var sideCommandRoots = []string{"/agents", "/board", "/mail", "/jobs"}

// isSideCommand reports whether line is an allowlisted mid-run command.
func isSideCommand(line string) bool {
	for _, root := range sideCommandRoots {
		if line == root || strings.HasPrefix(line, root+" ") {
			return true
		}
	}
	return false
}

// onSideCommand handles one classified line from the stdin reader
// goroutine: execute now under a paused display, or queue for the next
// turn boundary when no live display is active (e.g. a security prompt
// owns the terminal — printing into its card would corrupt it).
func (a *AgentMode) onSideCommand(ctx context.Context, line string) {
	timer := a.turnTimer
	if timer != nil && timer.IsRunning() {
		timer.Pause()
		fmt.Println(colorize("  ⚡ "+line, ColorCyan))
		a.execSideCommand(ctx, line)
		timer.Resume()
		return
	}
	a.sideCmdMu.Lock()
	a.sideCmdQueue = append(a.sideCmdQueue, line)
	a.sideCmdMu.Unlock()
}

// applySideCommands runs every queued side command. Called at the turn
// boundary (before the type-ahead drain, so a /mail send lands in this
// turn's inbox drain).
func (a *AgentMode) applySideCommands(ctx context.Context) {
	a.sideCmdMu.Lock()
	queued := a.sideCmdQueue
	a.sideCmdQueue = nil
	a.sideCmdMu.Unlock()
	if len(queued) == 0 {
		return
	}
	fmt.Println(colorize("  ⚡ "+i18n.T("agent.sidecmd.applying", len(queued)), ColorCyan))
	for _, line := range queued {
		fmt.Println(colorize("  ⚡ "+line, ColorCyan))
		a.execSideCommand(ctx, line)
	}
}

// execSideCommand routes one line through the command handler (or the test
// seam). The allowlisted handlers ignore their context and never panic
// mode-switch sentinels.
func (a *AgentMode) execSideCommand(ctx context.Context, line string) {
	if a.sideCmdExec != nil {
		a.sideCmdExec(line)
		return
	}
	if a.cli == nil || a.cli.commandHandler == nil {
		return
	}
	a.cli.commandHandler.HandleCommand(ctx, line)
}
