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
 *
 * Persisting them is what keeps the prefix cache alive — dropping a
 * message the previous request sent moves every byte after it — but it
 * also meant one block per turn accumulating in the window for the whole
 * session, most of them repeating what the block before them already
 * said. So the block is not injected at all when it would repeat the last
 * one still in history: the conversation keeps growing by appends only,
 * the cache holds, and the model reads the same information once instead
 * of once per turn.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// lastTurnContextText returns the text of the most recent turn-context
// message in the history, or "" when the history carries none.
func lastTurnContextText(history []models.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].IsTurnContext() {
			return history[i].Content
		}
	}
	return ""
}

// turnContextIsRedundant reports whether a block would tell the model
// exactly what the last one already did.
//
// Comparison is on the whole text, so any real change — the day rolling
// over, a new working directory, an MCP channel push, a recall block —
// makes the block travel again. Only a byte-for-byte repeat is skipped.
//
// The question is whether the model still READS the earlier block, not
// whether the array still holds it. On a provider that takes turn-scoped
// system messages the two stopped being the same thing: an earlier block
// stays in the array — it has to, or the prompt cache and the
// thinking-block conversation check both break — but it renders only
// while no user message follows it, and every later turn puts one there.
// Nothing already sent is visible, so nothing can be redundant, and
// skipping meant the model read its context once and never again. That is
// what the capability is for: a cleared block costs no tokens, so sending
// one every turn is the shape the feature describes.
func turnContextIsRedundant(provider, model string, history []models.Message, text string) bool {
	if strings.TrimSpace(text) == "" {
		return true
	}
	if client.SupportsTurnScopedSystem(provider, model) {
		return false
	}
	return text == lastTurnContextText(history)
}

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

// dropSupersededTurnContext removes every turn-context message except the
// most recent one.
//
// Compaction is the one moment where dropping them is free: the history is
// being rewritten anyway, so the prefix cache is already being rebuilt and
// nothing is lost by not carrying ChatCLI's own repeated preamble into the
// summary. The newest block survives because it is the one still
// describing the session the model is in.
//
// Returns the input unchanged when there is nothing to drop, so the caller
// never pays a copy for the common case.
func dropSupersededTurnContext(history []models.Message) []models.Message {
	last := -1
	count := 0
	for i, m := range history {
		if m.IsTurnContext() {
			last = i
			count++
		}
	}
	if count < 2 {
		return history
	}
	out := make([]models.Message, 0, len(history)-count+1)
	for i, m := range history {
		if m.IsTurnContext() && i != last {
			continue
		}
		out = append(out, m)
	}
	return out
}
