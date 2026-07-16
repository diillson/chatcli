/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
)

// TestHubSyncStartResume_AdoptsActiveConversation pins the MCP continuity
// model: resume joins the principal's ACTIVE conversation (the one the
// REPL/gateway is on) instead of rotating to a fresh one, and the first pull
// splices the other channel's turns.
func TestHubSyncStartResume_AdoptsActiveConversation(t *testing.T) {
	store, err := hub.OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	// A "REPL" writes two turns on the shared conversation.
	repl := newHubSync(newLocalHubClient(store, "user-a"), zap.NewNop())
	if err := repl.startFresh(ctx); err != nil {
		t.Fatalf("repl startFresh: %v", err)
	}
	repl.mirrorTurn(ctx, "repl question", "repl answer")
	replConv, _ := repl.status()

	// The "MCP server" resumes: same conversation, no rotation.
	mcp := newHubSync(newLocalHubClient(store, "user-a"), zap.NewNop())
	if err := mcp.startResume(ctx); err != nil {
		t.Fatalf("startResume: %v", err)
	}
	mcpConv, principal := mcp.status()
	if mcpConv != replConv {
		t.Fatalf("resume must adopt the active conversation: repl=%q mcp=%q", replConv, mcpConv)
	}
	if principal != "user-a" {
		t.Fatalf("principal = %q", principal)
	}

	// The REPL's turns arrive on the first pull. They were written on the
	// "local" channel by ANOTHER process, but this HubSync instance never
	// wrote them — hub events tag channel, not process. Since both sides
	// call their own writes "local", simulate the gateway shape instead:
	// append one event on a distinct channel and verify it is pulled.
	if _, err := store.Append(ctx, models.ConversationEvent{
		ConvID: replConv, Channel: "telegram", Role: models.ConvRoleUser, Content: "from telegram",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	msgs := mcp.pull(ctx)
	found := false
	for _, m := range msgs {
		if m.Content == "from telegram" {
			found = true
		}
	}
	if !found {
		t.Fatalf("resumed sync must pull cross-channel turns; got %+v", msgs)
	}
}

// TestHubSyncStartResume_BoundsBacklog proves the watermark bound: with a
// long backlog only the newest hubResumeMaxEvents land in the first pull.
func TestHubSyncStartResume_BoundsBacklog(t *testing.T) {
	store, err := hub.OpenSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	writer := newLocalHubClient(store, "user-b")
	conv, _, err := writer.ResolveActiveConversation(ctx, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	total := hubResumeMaxEvents * 2
	for i := 0; i < total; i++ {
		if _, err := writer.AppendEvent(ctx, models.ConversationEvent{
			ConvID: conv, Channel: "telegram", Role: models.ConvRoleUser,
			Content: fmt.Sprintf("msg-%03d", i),
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	mcp := newHubSync(newLocalHubClient(store, "user-b"), zap.NewNop())
	if err := mcp.startResume(ctx); err != nil {
		t.Fatalf("startResume: %v", err)
	}
	msgs := mcp.pull(ctx)
	if len(msgs) != hubResumeMaxEvents {
		t.Fatalf("first pull = %d messages, want %d (bounded tail)", len(msgs), hubResumeMaxEvents)
	}
	if msgs[0].Content != fmt.Sprintf("msg-%03d", total-hubResumeMaxEvents) {
		t.Errorf("tail must start at the bound: first = %q", msgs[0].Content)
	}
}
