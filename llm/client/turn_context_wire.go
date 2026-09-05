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
 * Placement is the whole trick. A turn-scoped message renders only while
 * no user message follows it, so emitting the block where the history
 * holds it — ahead of the user's turn — would clear it on the very
 * request that introduced it. It is emitted one position later, directly
 * after the message it accompanies. That position is stable for the life
 * of the conversation: the same history always serializes to the same
 * bytes, which is what re-sending a cleared message verbatim requires. A
 * block that lands at a different index on a later request is an edit to
 * an earlier message, and misses the cache from there on.
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

// Flush returns what was claimed and clears it. Callers call it right
// after appending an ordinary message, which is what places the block one
// position later than the history holds it.
func (e *TurnContextEmitter) Flush() []TurnScopedSystem {
	if e == nil || len(e.pending) == 0 {
		return nil
	}
	out := e.pending
	e.pending = nil
	e.emitted = true
	return out
}

// Used reports whether any turn-scoped message was actually emitted, so
// the beta header travels only on requests that carry one instead of
// opting every turn into a beta.
func (e *TurnContextEmitter) Used() bool { return e != nil && e.emitted }
