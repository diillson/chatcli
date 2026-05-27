/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

func newTestHubSessions(t *testing.T) (*hubSessions, hub.Store) {
	t.Helper()
	store, err := hub.OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newHubSessions(store, zap.NewNop()), store
}

func TestHubSessionsBeginFinishPersists(t *testing.T) {
	hs, store := newTestHubSessions(t)
	ctx := context.Background()
	msg := gateway.InboundMessage{Platform: "telegram", ChatID: "42", UserID: "u1", Text: "ping"}

	turn := hs.begin(ctx, msg)
	if turn.convID == "" {
		t.Fatal("begin did not resolve a conversation")
	}
	if turn.preamble != "" {
		t.Fatalf("first turn should have empty preamble, got %q", turn.preamble)
	}
	turn.finish(ctx, "pong")

	events, err := store.Read(ctx, turn.convID, 0, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 || events[0].Content != "ping" || events[1].Content != "pong" {
		t.Fatalf("expected [ping, pong], got %+v", events)
	}

	// A second turn must carry the first turn as preamble context.
	turn2 := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "42", UserID: "u1", Text: "again"})
	if !strings.Contains(turn2.preamble, "ping") || !strings.Contains(turn2.preamble, "pong") {
		t.Fatalf("preamble missing prior dialog: %q", turn2.preamble)
	}
}

func TestHubSessionsCrossChannelContinuityViaBinding(t *testing.T) {
	hs, store := newTestHubSessions(t)
	ctx := context.Background()

	// Bind both a Telegram identity and the local CLI identity to "alice".
	if err := store.Bind(ctx, "telegram", "u1", "alice"); err != nil {
		t.Fatalf("Bind telegram: %v", err)
	}
	if err := store.Bind(ctx, "local", "alice", "alice"); err != nil {
		t.Fatalf("Bind local: %v", err)
	}

	// A message via Telegram...
	tTurn := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "42", UserID: "u1", Text: "from telegram"})
	tTurn.finish(ctx, "ack telegram")

	// ...and one via the local channel land on the SAME conversation.
	lTurn := hs.begin(ctx, gateway.InboundMessage{Platform: "local", ChatID: "cli", UserID: "alice", Text: "from notebook"})
	if lTurn.convID != tTurn.convID {
		t.Fatalf("cross-channel continuity broken: telegram conv %q != local conv %q", tTurn.convID, lTurn.convID)
	}
	if !strings.Contains(lTurn.preamble, "from telegram") {
		t.Fatalf("notebook turn missing telegram context: %q", lTurn.preamble)
	}
}

func TestHubSessionsUnboundSendersAreIsolated(t *testing.T) {
	hs, _ := newTestHubSessions(t)
	ctx := context.Background()

	a := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "1", UserID: "alice", Text: "hi"})
	b := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "2", UserID: "bob", Text: "hi"})
	if a.convID == b.convID {
		t.Fatal("unbound senders must not share a conversation")
	}
	if a.principal == b.principal {
		t.Fatal("unbound senders must get distinct per-channel principals")
	}
}

func TestHubSessionsLoadBindings(t *testing.T) {
	hs, store := newTestHubSessions(t)
	ctx := context.Background()
	t.Setenv("CHATCLI_HUB_BINDINGS", "telegram:u1=alice; slack:U9=alice , bogus-entry")

	hs.loadBindings(ctx)

	if p, err := store.ResolvePrincipal(ctx, "telegram", "u1"); err != nil || p != "alice" {
		t.Fatalf("telegram binding not loaded: %q, %v", p, err)
	}
	if p, err := store.ResolvePrincipal(ctx, "slack", "U9"); err != nil || p != "alice" {
		t.Fatalf("slack binding not loaded: %q, %v", p, err)
	}
}

// TestGatewayCoLocationPublishesToSubscribers proves the co-location guarantee:
// when the gateway shares the server's fan-out Manager (one process), a turn it
// records publishes live to a subscriber — i.e. a connected notebook sees a
// Telegram message in real time.
func TestGatewayCoLocationPublishesToSubscribers(t *testing.T) {
	store, err := hub.OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	mgr := hub.NewManager(store, nil, 16)
	hs := newHubSessions(mgr, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A connected notebook subscribes to the (unbound) sender's conversation.
	conv, _ := mgr.Resolve(ctx, "telegram:u1")
	stream, err := mgr.Subscribe(ctx, conv, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// A Telegram message flows through the co-located gateway.
	turn := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "1", UserID: "u1", Text: "olá"})
	turn.finish(ctx, "oi de volta")

	want := []string{"olá", "oi de volta"}
	for i, w := range want {
		select {
		case ev := <-stream:
			if ev.Content != w {
				t.Fatalf("event %d = %q, want %q", i, ev.Content, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("subscriber did not receive event %d (%q)", i, w)
		}
	}
}

func TestHubSessionsNilStoreDegradesGracefully(t *testing.T) {
	hs := newHubSessions(nil, zap.NewNop())
	ctx := context.Background()
	turn := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "1", UserID: "u", Text: "hi"})
	if turn.preamble != "" || turn.convID != "" {
		t.Fatal("nil-store begin should yield an empty turn")
	}
	turn.finish(ctx, "reply") // must not panic
}
