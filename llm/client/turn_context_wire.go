/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * How per-turn context reaches the model, decided per provider.
 *
 * ChatCLI carries per-turn context — the date, proactive recall,
 * auto-activated skills, channel pushes — as a flagged user-role message
 * appended right before the user's turn. Appending is what holds the
 * prompt cache, since nothing before it moves, but the block then sits in
 * the window for the rest of the session.
 *
 * A provider that accepts a role:"system" message inside messages can do
 * better: the block goes in as an instruction rather than as the user's
 * words, and clear_at scopes it to the turn it belongs to — it renders
 * once and, from the next user message on, stays in the array without
 * rendering, at no token cost. It has to stay: the array is what the
 * prompt cache and, on models that check it, the thinking-block
 * conversation are matched against.
 *
 * The decision lives here rather than in one provider's client because
 * nothing about it is provider-specific except the answer. It is read from
 * the model catalog, so every surface that serves a capable model — the
 * first-party API and the Bedrock mirror alike — takes the same path, and
 * a provider that ships an equivalent later is one catalog flag away.
 * Every provider whose catalog entry does not claim it keeps the
 * user-role block byte for byte, so this can only add.
 *
 * Placement answers to two rules at once, and the provider enforces one
 * of them:
 *
 *   messages.N: role 'system' must precede an 'assistant' message or end
 *   the array
 *
 * So a block may sit immediately before an assistant turn, or last. It may
 * never sit before a user turn — and a tool loop is full of user-role
 * messages, because that is how tool results travel.
 *
 * The second rule is what makes the block useful: it renders only while no
 * user message follows it. Emitting it where the history holds it, ahead
 * of the user's turn, would clear it on the very request that introduced
 * it.
 *
 * Both are satisfied by holding the block until the next assistant turn is
 * about to be written, and by flushing whatever is left at the end of the
 * array. A block before an assistant turn is legal and, since a later user
 * message always follows, correctly cleared. A block at the end is legal
 * and renders, which is the copy the current turn reads.
 *
 * Position stays a function of the history alone, which is what re-sending
 * a cleared message verbatim requires: a block that lands at a different
 * index on a later request is an edit to an earlier message, and misses
 * the cache from there on.
 */
package client

import (
	"strings"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
)

// MidConversationSystemCapability is the catalog flag for a model that
// accepts role:"system" messages inside messages.
const MidConversationSystemCapability = "mid_conversation_system"

// TurnScopedSystemBeta enables clear_at on those messages. Without it the
// field is rejected as an unknown field, so it travels only on a request
// that actually carries one.
const TurnScopedSystemBeta = "mid-conversation-system-clear-at-2026-08-21"

// ClearAtNextUserMessage scopes a system message to the turn it was
// appended on.
const ClearAtNextUserMessage = "next_user_message"

// TurnScopedSystem is the wire form of one turn-scoped system message.
//
// Content is plain text: a turn-scoped message takes text blocks only —
// tool additions and removals, output_config and cache_control are all
// rejected on one. None of that costs anything here, since the point of
// the message is to sit after the cached prefix rather than inside it.
type TurnScopedSystem struct {
	Role    string
	ClearAt string
	Content string
}

// SupportsTurnScopedSystem reports whether this provider and model take a
// turn-scoped system message. Everything else keeps the user-role block.
func SupportsTurnScopedSystem(provider, model string) bool {
	if strings.TrimSpace(model) == "" {
		return false
	}
	return catalog.HasCapability(provider, model, MidConversationSystemCapability)
}

// TurnContextEmitter defers turn-context blocks by one position while a
// provider's message array is built. A model without the capability
// yields an emitter that claims nothing, so its caller's loop is
// unchanged.
type TurnContextEmitter struct {
	enabled bool
	emitted bool
	pending []TurnScopedSystem
}

// NewTurnContextEmitter builds the emitter for one request.
func NewTurnContextEmitter(provider, model string) *TurnContextEmitter {
	return &TurnContextEmitter{enabled: SupportsTurnScopedSystem(provider, model)}
}

// Claim takes a message the emitter will send as a turn-scoped system
// message, reporting false when the caller should render it as it always
// has.
func (e *TurnContextEmitter) Claim(msg models.Message) bool {
	if e == nil || !e.enabled || !msg.IsTurnContext() || msg.Content == "" {
		return false
	}
	// Images cannot ride on a system message; a block carrying them is
	// rendered the old way rather than losing them.
	if len(msg.Images) > 0 {
		return false
	}
	e.pending = append(e.pending, TurnScopedSystem{
		Role:    "system",
		ClearAt: ClearAtNextUserMessage,
		Content: msg.Content,
	})
	return true
}

// FlushBefore returns what must be written immediately ahead of a message
// with this role, and clears it. Only an assistant turn may be preceded by
// a system message; before anything else the block waits, which is what
// keeps a tool loop legal — tool results travel as user-role messages, and
// a block written before one is rejected outright.
func (e *TurnContextEmitter) FlushBefore(role string) []TurnScopedSystem {
	if e == nil || len(e.pending) == 0 {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(role), "assistant") {
		return nil
	}
	return e.take()
}

// Flush returns whatever is still pending, for the end of the array — the
// one other position the provider accepts, and the one where the block
// renders for the turn being sent.
func (e *TurnContextEmitter) Flush() []TurnScopedSystem {
	if e == nil || len(e.pending) == 0 {
		return nil
	}
	return e.take()
}

// take returns the pending blocks as a single message.
//
// Coalesced because the provider's rule leaves at most one legal slot in
// the shapes that pile blocks up: a system message must precede an
// assistant turn or end the array, and the first of two adjacent system
// messages does neither. Blocks pile up when several turns pass with no
// assistant between them — which is what a failed request leaves behind,
// and what a run of tool results looks like.
//
// Joining is deterministic given the history, so the position and the
// bytes are still a function of the conversation alone.
func (e *TurnContextEmitter) take() []TurnScopedSystem {
	pending := e.pending
	e.pending = nil
	e.emitted = true
	if len(pending) == 1 {
		return pending
	}
	parts := make([]string, 0, len(pending))
	for _, p := range pending {
		parts = append(parts, p.Content)
	}
	return []TurnScopedSystem{{
		Role:    "system",
		ClearAt: ClearAtNextUserMessage,
		Content: strings.Join(parts, "\n\n"),
	}}
}

// Used reports whether any turn-scoped message was actually emitted, so
// the beta header travels only on requests that carry one instead of
// opting every turn into a beta.
func (e *TurnContextEmitter) Used() bool { return e != nil && e.emitted }
