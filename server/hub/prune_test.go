/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package hub

import (
	"context"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

// TestNewConversationPrunesPrior verifies the bounded-buffer guarantee: rotating
// to a fresh conversation deletes the one it replaces, so the database does not
// accumulate dead threads.
func TestNewConversationPrunesPrior(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	prior, _ := st.Resolve(ctx, "alice")
	if _, err := st.Append(ctx, models.ConversationEvent{ConvID: prior, Principal: "alice", Channel: "telegram", Role: models.ConvRoleUser, Content: "old"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	fresh, err := st.NewConversation(ctx, "alice")
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	if fresh == prior {
		t.Fatal("did not rotate")
	}

	// The prior conversation and its events are gone.
	if evs, _ := st.Read(ctx, prior, 0, 0); len(evs) != 0 {
		t.Fatalf("prior events not pruned: %d remain", len(evs))
	}
	if _, err := st.OwnerOf(ctx, prior); err == nil {
		t.Fatal("prior conversation row not pruned")
	}
	// The fresh one is active and intact.
	if owner, err := st.OwnerOf(ctx, fresh); err != nil || owner != "alice" {
		t.Fatalf("fresh conversation missing: owner=%q err=%v", owner, err)
	}
}

// TestPurgeIdleProtectsActive ensures PurgeIdle never deletes a principal's
// active conversation, even with an aggressive cutoff.
func TestPurgeIdleProtectsActive(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	conv, _ := st.Resolve(ctx, "alice")
	if _, err := st.PurgeIdle(ctx, time.Millisecond); err != nil {
		t.Fatalf("PurgeIdle: %v", err)
	}
	if owner, err := st.OwnerOf(ctx, conv); err != nil || owner != "alice" {
		t.Fatalf("active conversation was purged: owner=%q err=%v", owner, err)
	}
}

func TestPurgeIdleDisabledWhenZero(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if n, err := st.PurgeIdle(ctx, 0); err != nil || n != 0 {
		t.Fatalf("PurgeIdle(0) = %d,%v; want 0,nil", n, err)
	}
}
