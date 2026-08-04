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

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

func gatewayBindingHarness(t *testing.T) (*ChatCLI, *hubSessions, context.Context) {
	t.Helper()
	c := bindingTestCLI(t)
	ctx := context.Background()
	store, err := hub.OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "hub.db"), zap.NewNop())
	if err != nil {
		t.Fatalf("hub store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return c, newHubSessions(store, zap.NewNop()), ctx
}

func gwMsg(text string) gateway.InboundMessage {
	return gateway.InboundMessage{Platform: "telegram", ChatID: "1", UserID: "1", Text: text}
}

// Channel /session commands: attach persists the binding (settings survive a
// hubSessions restart), status reports it, detach clears it, and plain text
// is never hijacked.
func TestGatewaySessionCommand_BindingLifecycle(t *testing.T) {
	c, sessions, ctx := gatewayBindingHarness(t)

	if _, handled := c.handleGatewaySessionCommand(ctx, sessions, gwMsg("/sessions are nice")); handled {
		t.Fatal("plain text starting with /session-ish token must not be hijacked")
	}

	reply, handled := c.handleGatewaySessionCommand(ctx, sessions, gwMsg("/session attach projeto-x"))
	if !handled || !strings.Contains(reply, "projeto-x") {
		t.Fatalf("attach not handled correctly: %q handled=%v", reply, handled)
	}
	principal := sessions.principalFor(ctx, "telegram", "1")
	if got := sessions.sessionBindingFor(ctx, principal); got != "projeto-x" {
		t.Fatalf("binding not persisted, got %q", got)
	}

	// A new hubSessions over the same store must still see the binding.
	sessions2 := newHubSessions(sessions.store, zap.NewNop())
	if got := sessions2.sessionBindingFor(ctx, principal); got != "projeto-x" {
		t.Fatalf("binding must survive a daemon restart, got %q", got)
	}

	// Turn write-through: the file carries the pair, and the preamble for
	// the next turn comes from it.
	c.appendNamedSessionTurn("projeto-x", "monta o relatório", "feito")
	pre := c.renderNamedSessionPreamble("projeto-x")
	if !strings.Contains(pre, "monta o relatório") || !strings.Contains(pre, "feito") {
		t.Fatalf("preamble must reflect the written-through turn, got %q", pre)
	}

	if reply, _ := c.handleGatewaySessionCommand(ctx, sessions, gwMsg("/session status")); !strings.Contains(reply, "projeto-x") {
		t.Fatalf("status must report the binding, got %q", reply)
	}
	if _, handled := c.handleGatewaySessionCommand(ctx, sessions, gwMsg("/session detach")); !handled {
		t.Fatal("detach must be handled")
	}
	if got := sessions.sessionBindingFor(ctx, principal); got != "" {
		t.Fatalf("detach must clear the binding, got %q", got)
	}

	// delete is deliberately not a channel command: it must fall into the
	// usage reply, and the store must keep the session.
	c2 := c.sessionManager
	if !c2.SessionExists("projeto-x") {
		t.Fatal("precondition: session file exists")
	}
	reply, handled = c.handleGatewaySessionCommand(ctx, sessions, gwMsg("/session delete projeto-x"))
	if !handled || !c2.SessionExists("projeto-x") {
		t.Fatalf("delete from a channel must be refused (usage reply), got %q", reply)
	}
}

// save snapshots the hub conversation into the store and binds.
func TestGatewaySessionCommand_SaveSnapshotsHub(t *testing.T) {
	c, sessions, ctx := gatewayBindingHarness(t)
	principal := sessions.principalFor(ctx, "telegram", "1")
	convID, err := sessions.store.Resolve(ctx, principal)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, ev := range []models.ConversationEvent{
		{ConvID: convID, Principal: principal, Channel: "telegram", Role: models.ConvRoleUser, Content: "oi"},
		{ConvID: convID, Principal: principal, Channel: "telegram", Role: models.ConvRoleAssistant, Content: "olá!"},
	} {
		if _, err := sessions.store.Append(ctx, ev); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	reply, handled := c.handleGatewaySessionCommand(ctx, sessions, gwMsg("/session save zap-thread"))
	if !handled || !strings.Contains(reply, "zap-thread") {
		t.Fatalf("save not handled: %q", reply)
	}
	sd, err := c.sessionManager.LoadSessionV2("zap-thread")
	if err != nil || len(sd.ChatHistory) != 2 {
		t.Fatalf("snapshot must carry the 2 hub turns, err=%v n=%d", err, len(sd.ChatHistory))
	}
	if got := sessions.sessionBindingFor(ctx, principal); got != "zap-thread" {
		t.Fatalf("save must bind, got %q", got)
	}
}
