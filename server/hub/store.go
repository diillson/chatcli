/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package hub holds the durable source of truth for cross-channel
// conversations. It lets messages from any provider (Telegram, Slack,
// WhatsApp) and the local CLI flow into a single shared conversation per
// principal, so a thread started on one channel continues seamlessly on
// another until the user starts a new session.
package hub

import (
	"context"
	"errors"
	"time"

	"github.com/diillson/chatcli/models"
)

// ErrUnboundChannel is returned when a channel identity has no principal
// binding. The Hub quarantines such identities rather than guessing an owner,
// preventing one person's messages from leaking into another's conversation.
var ErrUnboundChannel = errors.New("hub: channel identity is not bound to a principal")

// Binding maps a per-platform channel identity to a principal.
type Binding struct {
	Platform  string
	UserID    string
	Principal string
}

// Store is the durable, concurrency-safe source of truth for cross-channel
// conversations: an append-only event log per conversation, a per-principal
// "active conversation" pointer, and channel-identity → principal bindings.
//
// Reads are safe for concurrent use. Writes are serialized internally, so a
// Telegram adapter and a notebook CLI may append to the same conversation
// without clobbering one another; each append receives a server-assigned,
// monotonically increasing Seq.
type Store interface {
	// Resolve returns the active conversation id for a principal, creating a
	// fresh conversation (and pointer) the first time it is seen.
	Resolve(ctx context.Context, principal string) (convID string, err error)

	// NewConversation rotates the active-conversation pointer for a principal
	// to a brand-new conversation and returns its id. This is what /newsession
	// triggers; every channel resolving that principal afterwards lands on the
	// new conversation.
	NewConversation(ctx context.Context, principal string) (convID string, err error)

	// Append writes an event, assigning its Seq, and returns the stored event.
	// If ev.ClientMsgID is non-empty and already present in the conversation,
	// the existing event is returned unchanged (idempotent retry).
	Append(ctx context.Context, ev models.ConversationEvent) (models.ConversationEvent, error)

	// Read returns events of a conversation with Seq strictly greater than
	// sinceSeq, ordered by Seq ascending, capped at limit (limit <= 0 = all).
	Read(ctx context.Context, convID string, sinceSeq int64, limit int) ([]models.ConversationEvent, error)

	// ResolvePrincipal maps a channel identity to its principal, or returns
	// ErrUnboundChannel when the identity has not been bound.
	ResolvePrincipal(ctx context.Context, platform, userID string) (principal string, err error)

	// Bind associates a channel identity with a principal (idempotent upsert).
	Bind(ctx context.Context, platform, userID, principal string) error

	// ListBindings returns bindings, optionally filtered to one principal
	// (empty principal = all).
	ListBindings(ctx context.Context, principal string) ([]Binding, error)

	// OwnerOf returns the principal that owns a conversation, for authorization
	// checks: a subscriber must own the conversation it tails.
	OwnerOf(ctx context.Context, convID string) (principal string, err error)

	// PurgeIdle deletes conversations idle longer than olderThan (and their
	// events), keeping the hub bounded. The active conversation of each
	// principal is never purged. Returns how many were removed.
	PurgeIdle(ctx context.Context, olderThan time.Duration) (int, error)

	// Close releases the underlying database.
	Close() error
}
