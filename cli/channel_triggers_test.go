/*
 * ChatCLI - tests for channel trigger plumbing
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"strings"
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

func TestChannelTriggers_LoadRulesFromDisk(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "triggers.json")
	contents := `{"rules":[{"name":"disk-rule","channel":"ci","mode":"notify"}]}`
	if err := os.WriteFile(rulesPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write rules: %v", err)
	}

	// Override the rules path to point at the temp file, then reload.
	cli.channelTriggers.rulesPath = rulesPath
	n, err := cli.reloadChannelTriggerRules()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 rule loaded, got %d", n)
	}
	rules := cli.channelTriggerRules()
	if rules[0].Name != "disk-rule" {
		t.Errorf("Rules[0].Name = %q, want disk-rule", rules[0].Name)
	}
}

func TestChannelTriggers_LoadRulesMissingFileIsClean(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	cli.channelTriggers.rulesPath = filepath.Join(t.TempDir(), "nonexistent.json")
	n, err := cli.reloadChannelTriggerRules()
	if err != nil {
		t.Fatalf("missing file should be silent, got err: %v", err)
	}
	if n != 0 {
		t.Errorf("missing file → expected 0 rules, got %d", n)
	}
}

func TestChannelTriggers_LoadRulesBadSchema(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "triggers.json")
	contents := `{"rules":[{"name":"bad","mode":"auto"}]}` // auto without tools
	if err := os.WriteFile(rulesPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	cli.channelTriggers.rulesPath = rulesPath
	if _, err := cli.reloadChannelTriggerRules(); err == nil {
		t.Fatalf("expected validation error for auto without tools")
	}
}

func TestChannelTriggers_LoadRulesInvalidJSON(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "triggers.json")
	if err := os.WriteFile(rulesPath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	cli.channelTriggers.rulesPath = rulesPath
	if _, err := cli.reloadChannelTriggerRules(); err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestChannelTriggers_ConfirmInvalidID(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	if err := cli.channelTriggerConfirm(9999, true); err == nil {
		t.Fatalf("expected error for nonexistent confirm id")
	}
}

func TestChannelTriggers_RunMissingSeq(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	if err := cli.channelTriggerRun(9999); err == nil {
		t.Fatalf("expected error for nonexistent seq")
	}
}

func TestChannelTriggers_RenderTriggerLineHasContent(t *testing.T) {
	a := triggers.Action{
		Rule: triggers.Rule{Name: "rule-x"},
		Event: triggers.ChannelEvent{
			ServerName: "srv", Channel: "ch", Content: "hello world",
		},
	}
	line := renderTriggerLine(a)
	if !strings.Contains(line, "srv/ch") || !strings.Contains(line, "rule-x") || !strings.Contains(line, "hello world") {
		t.Errorf("line missing pieces: %q", line)
	}
}

func TestChannelTriggers_RenderBannerWithUnreadAndPending(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	if err := cli.channelTriggers.engine.SetRules([]triggers.Rule{
		{Name: "n", Channel: "ci", Mode: triggers.ModeNotify},
	}); err != nil {
		t.Fatal(err)
	}
	cli.mcpManager.Channels().Push(mcp.ChannelMessage{
		ServerName: "s", Channel: "ci", Content: "hello",
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cli.channelTriggers.mu.Lock()
		got := len(cli.channelTriggers.pendingNotify)
		cli.channelTriggers.mu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cli.renderChannelTriggerBanner() {
		t.Errorf("renderChannelTriggerBanner returned false despite pending notify")
	}
}

func TestChannelTriggers_RenderBannerEmptyReturnsFalse(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	if cli.renderChannelTriggerBanner() {
		t.Errorf("renderChannelTriggerBanner with empty state should return false")
	}
}

func TestChannelTriggers_DrainPendingAutoEmptyReturnsFalse(t *testing.T) {
	cli := newTestCLIWithMCP(t)
	if cli.drainPendingAutoTriggers() {
		t.Errorf("drainPendingAutoTriggers with empty queue should return false")
	}
}

func TestChannelTriggers_NilGuards(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}

	// All nil-safe accessors must not panic when channelTriggers/mcpManager are nil.
	if cli.channelTriggerIsPaused() {
		t.Errorf("nil triggers → IsPaused must be false")
	}
	if cli.channelTriggerRules() != nil {
		t.Errorf("nil triggers → Rules must be nil")
	}
	if cli.drainPendingAutoTriggers() {
		t.Errorf("nil triggers → drain must return false")
	}
	if cli.renderChannelTriggerBanner() {
		t.Errorf("nil mcp → banner must return false")
	}
	cli.channelTriggerPause()     // no panic
	cli.channelTriggerResume()    // no panic
	cli.shutdownChannelTriggers() // no panic
	cli.initChannelTriggers()     // no mcp manager → no-op, no panic

	if _, err := cli.reloadChannelTriggerRules(); err == nil {
		t.Errorf("nil triggers → reload must return error")
	}
	if err := cli.channelTriggerConfirm(1, true); err == nil {
		t.Errorf("nil triggers → confirm must return error")
	}
	if err := cli.channelTriggerRun(1); err == nil {
		t.Errorf("nil mcp → run must return error")
	}
}
