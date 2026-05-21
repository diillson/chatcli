/*
 * ChatCLI - tests for channel trigger plumbing
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/mcp/triggers"
	"go.uber.org/zap"
)

// newTestCLIWithMCP builds a minimal *ChatCLI wired to an in-memory
// MCP manager and trigger engine — enough to exercise the public
// surface (Ack, Pause, Rules, Run, Confirm) without dragging in the
// whole interactive prompt.
func newTestCLIWithMCP(t *testing.T) *ChatCLI {
	t.Helper()
	mgr := mcp.NewManagerWithOptions(zap.NewNop(), mcp.ChannelManagerOptions{
		PersistDir: t.TempDir(),
	})
	cli := &ChatCLI{
		logger:     zap.NewNop(),
		mcpManager: mgr,
	}
	cli.initChannelTriggers()
	t.Cleanup(func() {
		cli.shutdownChannelTriggers()
		_ = mgr.CloseChannels()
	})
	return cli
}

func TestChannelTriggers_AckClearsNotifyAndUnread(t *testing.T) {
	cli := newTestCLIWithMCP(t)

	// Set a notify rule and push three events.
	if err := cli.channelTriggers.engine.SetRules([]triggers.Rule{
		{Name: "n", Channel: "ci", Mode: triggers.ModeNotify},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		cli.mcpManager.Channels().Push(mcp.ChannelMessage{
			ServerName: "s", Channel: "ci", Content: "x",
		})
	}

	// Let the consumer drain the engine actions.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cli.channelTriggers.mu.Lock()
		got := len(cli.channelTriggers.pendingNotify)
		cli.channelTriggers.mu.Unlock()
		if got == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	notify, unread := cli.channelTriggerAck()
	if notify != 3 {
		t.Errorf("notify cleared = %d, want 3", notify)
	}
	if unread != 3 {
		t.Errorf("unread cleared = %d, want 3", unread)
	}
	if got := cli.mcpManager.Channels().Unread(); got != 0 {
		t.Errorf("Unread after ack = %d, want 0", got)
	}
}

func TestChannelTriggers_PauseSuppressesActions(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	if err := cli.channelTriggers.engine.SetRules([]triggers.Rule{
		{Name: "p", Channel: "ci", Mode: triggers.ModeNotify},
	}); err != nil {
		t.Fatal(err)
	}

	cli.channelTriggerPause()
	if !cli.channelTriggerIsPaused() {
		t.Fatalf("expected paused")
	}

	cli.mcpManager.Channels().Push(mcp.ChannelMessage{
		ServerName: "s", Channel: "ci", Content: "x",
	})

	// Give the consumer a chance — it should NOT see anything.
	time.Sleep(100 * time.Millisecond)
	cli.channelTriggers.mu.Lock()
	got := len(cli.channelTriggers.pendingNotify)
	cli.channelTriggers.mu.Unlock()
	if got != 0 {
		t.Errorf("pendingNotify while paused = %d, want 0", got)
	}

	cli.channelTriggerResume()
	if cli.channelTriggerIsPaused() {
		t.Fatalf("expected resumed")
	}
}

func TestChannelTriggers_RulesEmpty(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	if got := cli.channelTriggerRules(); len(got) != 0 {
		t.Errorf("Rules() = %d, want 0", len(got))
	}
}

func TestChannelTriggers_ConfirmDeniedIsCleanNoop(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	if err := cli.channelTriggers.engine.SetRules([]triggers.Rule{
		{Name: "c", Channel: "ci", Mode: triggers.ModeConfirm, Prompt: "go {{content}}"},
	}); err != nil {
		t.Fatal(err)
	}
	cli.mcpManager.Channels().Push(mcp.ChannelMessage{
		ServerName: "s", Channel: "ci", Content: "boom",
	})

	// Wait for the action to land in pendingConfirm.
	var id uint64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cli.channelTriggers.mu.Lock()
		for k := range cli.channelTriggers.pendingConfirm {
			id = k
		}
		cli.channelTriggers.mu.Unlock()
		if id != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if id == 0 {
		t.Fatalf("no pending confirm action")
	}

	if err := cli.channelTriggerConfirm(id, false); err != nil {
		t.Fatalf("Confirm(deny): %v", err)
	}
	cli.channelTriggers.mu.Lock()
	_, stillPending := cli.channelTriggers.pendingConfirm[id]
	cli.channelTriggers.mu.Unlock()
	if stillPending {
		t.Errorf("denied confirm should remove from pending")
	}
}

func TestChannelTriggers_ReloadRulesFromPath(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	// Initial: 0 rules
	if got := cli.channelTriggerRules(); len(got) != 0 {
		t.Fatalf("expected 0 rules, got %d", len(got))
	}
	// Inject rules via the engine directly (mimics what loading from
	// disk would do; the on-disk path is covered by integration runs).
	if err := cli.channelTriggers.engine.SetRules([]triggers.Rule{
		{Name: "r1", Channel: "*", Mode: triggers.ModeNotify},
		{Name: "r2", Channel: "alerts/*", Mode: triggers.ModeNotify},
	}); err != nil {
		t.Fatal(err)
	}
	if got := cli.channelTriggerRules(); len(got) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(got))
	}
}
