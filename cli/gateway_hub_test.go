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
	"go.uber.org/zap/zaptest/observer"
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

func TestHubSessionsUnboundSendersIsolatedWhenIsolateSet(t *testing.T) {
	t.Setenv("CHATCLI_HUB_ISOLATE", "true")
	hs, _ := newTestHubSessions(t)
	ctx := context.Background()

	a := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "1", UserID: "alice", Text: "hi"})
	b := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "2", UserID: "bob", Text: "hi"})
	if a.convID == b.convID {
		t.Fatal("with CHATCLI_HUB_ISOLATE, unbound senders must not share a conversation")
	}
	if a.principal == b.principal {
		t.Fatal("with CHATCLI_HUB_ISOLATE, unbound senders must get distinct per-channel principals")
	}
}

func TestHubSessionsUnboundSendersCollapseByDefault(t *testing.T) {
	// No CHATCLI_HUB_ISOLATE and no CHATCLI_HUB_PRINCIPAL: single-user default.
	t.Setenv("CHATCLI_HUB_ISOLATE", "")
	t.Setenv("CHATCLI_HUB_PRINCIPAL", "")
	hs, _ := newTestHubSessions(t)
	ctx := context.Background()

	a := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "1", UserID: "alice", Text: "hi"})
	b := hs.begin(ctx, gateway.InboundMessage{Platform: "whatsapp", ChatID: "2", UserID: "bob", Text: "hi"})
	if a.convID != b.convID {
		t.Fatal("by default, unbound senders should collapse into one shared conversation")
	}
	if a.principal != defaultHubPrincipal || b.principal != defaultHubPrincipal {
		t.Fatalf("expected shared default principal, got %q and %q", a.principal, b.principal)
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

	// Force single-user default (no isolation, default principal) so the
	// telegram sender and the notebook share one conversation.
	t.Setenv("CHATCLI_HUB_ISOLATE", "")
	t.Setenv("CHATCLI_HUB_PRINCIPAL", "")

	mgr := hub.NewManager(store, nil, 16)
	hs := newHubSessions(mgr, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A connected notebook subscribes to the shared conversation (the default
	// single-user principal that the unbound telegram sender also collapses to).
	conv, _ := mgr.Resolve(ctx, LocalHubPrincipal())
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

// TestHubSessionsBeginWithoutStoreWarnsOnce locks the observability contract:
// a daemon serving without the hub must say so in the log — exactly once, not
// per message, and never silently.
func TestHubSessionsBeginWithoutStoreWarnsOnce(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	hs := newHubSessions(nil, zap.New(core))
	ctx := context.Background()

	msg := gateway.InboundMessage{Platform: "telegram", ChatID: "1", UserID: "u", Text: "oi"}
	t1 := hs.begin(ctx, msg)
	t2 := hs.begin(ctx, msg)
	if t1 == nil || t2 == nil {
		t.Fatal("begin must degrade gracefully without a store")
	}
	warns := logs.FilterMessageSnippet("WITHOUT conversation hub").Len()
	if warns != 1 {
		t.Fatalf("expected exactly 1 no-hub warning, got %d", warns)
	}
}

// TestHubEnabledSource pins the provenance string for the hub on/off decision:
// db setting beats env, env beats default — the log line must name the layer
// that actually decided.
func TestHubEnabledSource(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CHATCLI_HUB_ENABLED", "")
	if got := hubEnabledSource(ctx, nil); got != "default" {
		t.Fatalf("no store, no env: source = %q, want default", got)
	}

	t.Setenv("CHATCLI_HUB_ENABLED", "false")
	if got := hubEnabledSource(ctx, nil); got != "env CHATCLI_HUB_ENABLED=false" {
		t.Fatalf("env source = %q", got)
	}

	store, err := hub.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetSetting(ctx, "enabled", "false"); err != nil {
		t.Fatal(err)
	}
	if got := hubEnabledSource(ctx, store); got != "db setting enabled=false" {
		t.Fatalf("db source = %q (db must beat env)", got)
	}
}
