/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The orchestrator's own progress: turns and tool calls of the run in
 * flight, mirrored to its registry entry. Workers reported theirs from
 * the start; the main loop never did, so /agents showed the orchestrator
 * stuck at turn zero and the skill ledger credited every run with no
 * turns and no tool calls.
 */
package cli

import "strings"

// noteRunTurn records the loop turn the run is on.
func (a *AgentMode) noteRunTurn(turn, maxTurns int) {
	if a == nil {
		return
	}
	a.runTurns = turn
	a.orchRun.SetTurn(turn, maxTurns)
}

// noteRunToolName records a tool the run executed, by plugin name.
func (a *AgentMode) noteRunToolName(name string) {
	if a == nil {
		return
	}
	name = normalizeToolName(name)
	if name == "" {
		return
	}
	if a.runToolNames == nil {
		a.runToolNames = map[string]bool{}
	}
	a.runToolNames[name] = true
}

// normalizeToolName lowercases a tool name and drops the @ prefix, so a
// skill's allowed-tools list and the executed name compare alike.
func normalizeToolName(name string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "@")
}

// noteRunToolCalls adds tool calls the run just executed.
func (a *AgentMode) noteRunToolCalls(n int) {
	if a == nil || n <= 0 {
		return
	}
	a.runToolCalls += n
	a.orchRun.AddToolCalls(n)
}
