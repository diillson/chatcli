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

// noteRunTurn records the loop turn the run is on.
func (a *AgentMode) noteRunTurn(turn, maxTurns int) {
	if a == nil {
		return
	}
	a.runTurns = turn
	a.orchRun.SetTurn(turn, maxTurns)
}

// noteRunToolCalls adds tool calls the run just executed.
func (a *AgentMode) noteRunToolCalls(n int) {
	if a == nil || n <= 0 {
		return
	}
	a.runToolCalls += n
	a.orchRun.AddToolCalls(n)
}
