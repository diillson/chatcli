/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package mcp

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"go.uber.org/zap"
)

// dynamicManager builds a manager with one connected server whose transport
// answers tools/list with the given payloads, in order.
func dynamicManager(t *testing.T, serverName string, listResults ...string) (*Manager, *mockTransport) {
	t.Helper()
	mt := &mockTransport{}
	for _, r := range listResults {
		mt.calls = append(mt.calls, mockCall{method: "tools/list", result: json.RawMessage(r)})
	}
	m := NewManagerWithOptions(zap.NewNop(), ChannelManagerOptions{PersistDir: t.TempDir()})
	m.servers[serverName] = &ServerConnection{
		Config:    ServerConfig{Name: serverName},
		transport: mt,
	}
	return m, mt
}

func TestRefreshServerTools_ReconcilesAddAndRemove(t *testing.T) {
	m, _ := dynamicManager(t, "http-toolkit",
		`{"tools":[{"name":"start","description":"bootstrap"},{"name":"send_request","description":"send an HTTP request"},{"name":"list_intercepted","description":"list traffic"}]}`,
	)
	// Simulate the initial discovery state: only the bootstrap tool.
	m.tools["start"] = &MCPTool{Name: "start", ServerName: "http-toolkit"}
	m.tools["other_server_tool"] = &MCPTool{Name: "other_server_tool", ServerName: "elsewhere"}

	added, removed, err := m.RefreshServerTools("http-toolkit")
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	sort.Strings(added)
	if len(added) != 2 || added[0] != "list_intercepted" || added[1] != "send_request" {
		t.Errorf("added = %v, want [list_intercepted send_request]", added)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want none", removed)
	}
	if _, ok := m.tools["send_request"]; !ok {
		t.Error("new tool must enter the registry")
	}
	if _, ok := m.tools["other_server_tool"]; !ok {
		t.Error("tools owned by other servers must never be touched")
	}
	if m.servers["http-toolkit"].Status.ToolCount != 3 {
		t.Errorf("ToolCount = %d, want 3", m.servers["http-toolkit"].Status.ToolCount)
	}
}

func TestRefreshServerTools_RemovesVanishedTools(t *testing.T) {
	m, _ := dynamicManager(t, "srv",
		`{"tools":[{"name":"keep","description":"stays"}]}`,
	)
	m.tools["keep"] = &MCPTool{Name: "keep", ServerName: "srv"}
	m.tools["gone"] = &MCPTool{Name: "gone", ServerName: "srv"}

	added, removed, err := m.RefreshServerTools("srv")
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if len(added) != 0 || len(removed) != 1 || removed[0] != "gone" {
		t.Errorf("added=%v removed=%v, want none added and [gone] removed", added, removed)
	}
	if _, still := m.tools["gone"]; still {
		t.Error("vanished tool must leave the registry")
	}
}

func TestRefreshServerTools_DisconnectedServer(t *testing.T) {
	m := NewManagerWithOptions(zap.NewNop(), ChannelManagerOptions{PersistDir: t.TempDir()})
	if _, _, err := m.RefreshServerTools("nope"); err == nil {
		t.Error("refreshing an unknown server must error, not panic")
	}
}

func TestListChangedNotification_TriggersDebouncedRefresh(t *testing.T) {
	old := dynamicToolRefreshDebounce
	dynamicToolRefreshDebounce = 10 * time.Millisecond
	defer func() { dynamicToolRefreshDebounce = old }()

	m, mt := dynamicManager(t, "http-toolkit",
		`{"tools":[{"name":"start"},{"name":"send_request"}]}`,
	)
	m.tools["start"] = &MCPTool{Name: "start", ServerName: "http-toolkit"}

	// Burst of notifications — must coalesce into ONE tools/list call.
	for i := 0; i < 5; i++ {
		m.Channels().ProcessSSENotification("http-toolkit",
			[]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`))
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.DrainToolListChanges()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	m.mu.RLock()
	_, hasNew := m.tools["send_request"]
	m.mu.RUnlock()
	if !hasNew {
		t.Fatal("registry must contain the new tool after the notification-driven refresh")
	}
	if mt.callIdx != 1 {
		t.Errorf("burst of 5 notifications must coalesce into 1 tools/list call, got %d", mt.callIdx)
	}
	// The notification must ALSO stay visible in the channel inbox.
	if got := m.Channels().GetByChannel("tools/list_changed", 10); len(got) == 0 {
		t.Error("list_changed must still land in the channel ring for auditability")
	}
}

func TestListChangedNotification_KillSwitch(t *testing.T) {
	t.Setenv("CHATCLI_MCP_DYNAMIC_TOOLS", "false")
	old := dynamicToolRefreshDebounce
	dynamicToolRefreshDebounce = 5 * time.Millisecond
	defer func() { dynamicToolRefreshDebounce = old }()

	m, mt := dynamicManager(t, "srv", `{"tools":[{"name":"x"}]}`)
	m.Channels().ProcessSSENotification("srv",
		[]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`))

	time.Sleep(50 * time.Millisecond)
	if mt.callIdx != 0 {
		t.Error("kill switch must prevent any refresh round-trip")
	}
	if len(m.DrainToolListChanges()) != 0 {
		t.Error("kill switch must record no changes")
	}
}

func TestDrainToolListChanges_DrainsOnce(t *testing.T) {
	m := NewManagerWithOptions(zap.NewNop(), ChannelManagerOptions{PersistDir: t.TempDir()})
	m.refreshMu.Lock()
	m.toolListChanges = []ToolListChange{{Server: "srv", Added: []string{"a"}}}
	m.refreshMu.Unlock()

	if got := m.DrainToolListChanges(); len(got) != 1 {
		t.Fatalf("first drain = %d changes, want 1", len(got))
	}
	if got := m.DrainToolListChanges(); len(got) != 0 {
		t.Errorf("second drain = %d changes, want 0", len(got))
	}
}

func TestHandledProtocolEventDoesNotInflateUnread(t *testing.T) {
	old := dynamicToolRefreshDebounce
	dynamicToolRefreshDebounce = 5 * time.Millisecond
	defer func() { dynamicToolRefreshDebounce = old }()

	m, _ := dynamicManager(t, "srv", `{"tools":[{"name":"x"}]}`)

	m.Channels().ProcessSSENotification("srv",
		[]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`))
	if got := m.Channels().Unread(); got != 0 {
		t.Errorf("handled list_changed must not bump unread, got %d", got)
	}
	// The event still lands in the ring for audit.
	if len(m.Channels().GetByChannel("tools/list_changed", 5)) != 1 {
		t.Error("handled event must remain visible in the ring")
	}

	// A regular server message still counts.
	m.Channels().ProcessSSENotification("srv",
		[]byte(`{"jsonrpc":"2.0","method":"notifications/alerts","params":{"msg":"disk"}}`))
	if got := m.Channels().Unread(); got != 1 {
		t.Errorf("regular messages must keep bumping unread, got %d", got)
	}
}

func TestListChangedWithKillSwitchStillCountsUnread(t *testing.T) {
	t.Setenv("CHATCLI_MCP_DYNAMIC_TOOLS", "off")
	m, _ := dynamicManager(t, "srv", `{"tools":[{"name":"x"}]}`)

	m.Channels().ProcessSSENotification("srv",
		[]byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`))
	if got := m.Channels().Unread(); got != 1 {
		t.Errorf("with the refresh disabled nothing handled the event — unread must be %d, got 1", got)
	}
}

func TestChannelManagerClear(t *testing.T) {
	m := NewManagerWithOptions(zap.NewNop(), ChannelManagerOptions{PersistDir: t.TempDir()})
	for i := 0; i < 3; i++ {
		m.Channels().Push(ChannelMessage{ServerName: "srv", Channel: "alerts", Content: "msg"})
	}
	if m.Channels().Unread() != 3 {
		t.Fatalf("setup: unread = %d, want 3", m.Channels().Unread())
	}

	dropped := m.Channels().Clear()
	if dropped != 3 {
		t.Errorf("Clear dropped = %d, want 3", dropped)
	}
	if m.Channels().Unread() != 0 || len(m.Channels().GetRecent(10)) != 0 {
		t.Error("Clear must empty the ring and reset unread")
	}

	// Post-clear pushes behave normally.
	m.Channels().Push(ChannelMessage{ServerName: "srv", Channel: "alerts", Content: "fresh"})
	if m.Channels().Unread() != 1 || len(m.Channels().GetRecent(10)) != 1 {
		t.Error("ring must keep working after Clear")
	}
}
