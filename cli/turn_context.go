/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Per-turn context as a user-turn message.
 *
 * Everything that changes from one turn to the next — the date, the
 * proactive memory and session recall, auto-activated skills, MCP channel
 * pushes, the watcher snapshot — used to trail the system message. Every
 * provider caches by prefix, so a system message that differs each turn
 * made every breakpoint after it miss: each turn re-wrote the whole
 * conversation into the cache and read nothing back. Those blocks now ride
 * as one flagged user-role message placed right before the user's turn,
 * persisted with the conversation so the next request replays identical
 * bytes; the system message is byte-stable for the session.
 */
package cli

// setPendingTurnContext stashes the turn context text between assembly and
// the commit of a successful chat turn.
func (cli *ChatCLI) setPendingTurnContext(text string) {
	if cli == nil {
		return
	}
	cli.pendingTurnContext = text
}

// takePendingTurnContext returns and clears the stashed text.
func (cli *ChatCLI) takePendingTurnContext() string {
	if cli == nil {
		return ""
	}
	t := cli.pendingTurnContext
	cli.pendingTurnContext = ""
	return t
}
