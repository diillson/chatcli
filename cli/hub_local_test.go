/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

// TestLocalHubClientResolveAppendRead covers the on-disk client used by local
// hub mode: it resolves with the configured principal and round-trips events.
func TestLocalHubClientResolveAppendRead(t *testing.T) {
	store, err := hub.OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	c := newLocalHubClient(store, "edilson")
	ctx := context.Background()

	conv, principal, err := c.ResolveActiveConversation(ctx, "")
	if err != nil || conv == "" || principal != "edilson" {
		t.Fatalf("resolve = %q,%q,%v", conv, principal, err)
	}
	if _, err := c.AppendEvent(ctx, models.ConversationEvent{ConvID: conv, Channel: hubChannelLocal, Role: models.ConvRoleUser, Content: "hi"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := c.ReadConversation(ctx, conv, 0, 0)
	if err != nil || len(got) != 1 || got[0].Content != "hi" || got[0].Principal != "edilson" {
		t.Fatalf("read = %+v, %v", got, err)
	}
}

// TestLocalHubPollingPicksUpExternalAppend proves the realtime path: a turn
// written by another process (a second handle on the same db, standing in for
// the gateway) surfaces through the polling subscription.
func TestLocalHubPollingPicksUpExternalAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cliStore, err := hub.OpenSQLiteStore(ctx, path, nil)
	if err != nil {
		t.Fatalf("open cli store: %v", err)
	}
	t.Cleanup(func() { _ = cliStore.Close() })
	gwStore, err := hub.OpenSQLiteStore(ctx, path, nil)
	if err != nil {
		t.Fatalf("open gw store: %v", err)
	}
	t.Cleanup(func() { _ = gwStore.Close() })

	c := newLocalHubClient(cliStore, "edilson")
	c.poll = 50 * time.Millisecond // snappy for the test

	conv, _, err := c.ResolveActiveConversation(ctx, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	stream, err := c.SubscribeConversation(ctx, conv, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// The "gateway" process appends a Telegram turn on the shared conversation.
	if _, err := gwStore.Append(ctx, models.ConversationEvent{ConvID: conv, Principal: "edilson", Channel: "telegram", Role: models.ConvRoleUser, Content: "from phone"}); err != nil {
		t.Fatalf("gw append: %v", err)
	}

	select {
	case ev := <-stream:
		if ev.Content != "from phone" || ev.Channel != "telegram" {
			t.Fatalf("unexpected polled event: %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("polling did not pick up the external append")
	}
}

// TestGatewayPrincipalCollapseSingleUser checks that, with CHATCLI_HUB_PRINCIPAL
// set, an unbound gateway sender and the local CLI resolve to the same shared
// principal — the zero-binding single-user setup.
func TestGatewayPrincipalCollapseSingleUser(t *testing.T) {
	store, err := hub.OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t.Setenv("CHATCLI_HUB_PRINCIPAL", "edilson")

	hs := newHubSessions(store, zap.NewNop())
	if got := hs.principalFor(context.Background(), "telegram", "7718033109"); got != "edilson" {
		t.Fatalf("unbound sender did not collapse to shared principal: %q", got)
	}
	// The local CLI uses the same principal, so both land on one conversation.
	if LocalHubPrincipal() != "edilson" {
		t.Fatalf("LocalHubPrincipal = %q", LocalHubPrincipal())
	}
}

func TestHubSettingsResolutionPrecedence(t *testing.T) {
	store, err := hub.OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	// Defaults (no setting, no env).
	t.Setenv(envHubPrincipal, "")
	t.Setenv(envHubIsolate, "")
	t.Setenv(envHubEnabled, "")
	if !resolveHubEnabled(ctx, store) || resolveHubIsolate(ctx, store) || resolveHubPrincipal(ctx, store) != defaultHubPrincipal {
		t.Fatal("unexpected defaults")
	}

	// Env layer.
	t.Setenv(envHubPrincipal, "fromenv")
	if resolveHubPrincipal(ctx, store) != "fromenv" {
		t.Fatal("env not applied")
	}

	// Setting layer wins over env.
	if err := store.SetSetting(ctx, hubKeyPrincipal, "fromsetting"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if err := store.SetSetting(ctx, hubKeyIsolate, "true"); err != nil {
		t.Fatalf("SetSetting isolate: %v", err)
	}
	if err := store.SetSetting(ctx, hubKeyEnabled, "false"); err != nil {
		t.Fatalf("SetSetting enabled: %v", err)
	}
	if resolveHubPrincipal(ctx, store) != "fromsetting" {
		t.Fatal("setting should win over env")
	}
	if !resolveHubIsolate(ctx, store) {
		t.Fatal("isolate setting not applied")
	}
	if resolveHubEnabled(ctx, store) {
		t.Fatal("enabled=false setting not applied")
	}

	// Reset falls back to env.
	if err := store.DeleteSetting(ctx, hubKeyPrincipal); err != nil {
		t.Fatalf("DeleteSetting: %v", err)
	}
	if resolveHubPrincipal(ctx, store) != "fromenv" {
		t.Fatal("reset should fall back to env")
	}
}

func TestHubSyncSettingsRoundTrip(t *testing.T) {
	store, err := hub.OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	hs := newHubSync(newLocalHubClient(store, "edilson"), zap.NewNop())
	ctx := context.Background()

	if err := hs.setSetting(ctx, hubKeyIsolate, "true"); err != nil {
		t.Fatalf("setSetting: %v", err)
	}
	got, ok := hs.allSettings(ctx)
	if !ok || got[hubKeyIsolate] != "true" {
		t.Fatalf("allSettings = %v, ok=%v", got, ok)
	}
	if err := hs.resetSetting(ctx, hubKeyIsolate); err != nil {
		t.Fatalf("resetSetting: %v", err)
	}
	got, _ = hs.allSettings(ctx)
	if _, present := got[hubKeyIsolate]; present {
		t.Fatalf("setting not reset: %v", got)
	}
}
