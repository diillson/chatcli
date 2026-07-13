/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package mcp

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestMCPTool_ReadOnlyHint(t *testing.T) {
	cases := []struct {
		name string
		tool MCPTool
		want bool
	}{
		{"no annotations", MCPTool{}, false},
		{"hint true", MCPTool{Annotations: map[string]interface{}{"readOnlyHint": true}}, true},
		{"hint false", MCPTool{Annotations: map[string]interface{}{"readOnlyHint": false}}, false},
		{"hint wrong type", MCPTool{Annotations: map[string]interface{}{"readOnlyHint": "yes"}}, false},
		{"unrelated annotations", MCPTool{Annotations: map[string]interface{}{"title": "X"}}, false},
	}
	for _, c := range cases {
		if got := c.tool.ReadOnlyHint(); got != c.want {
			t.Errorf("%s: ReadOnlyHint() = %v, want %v", c.name, got, c.want)
		}
	}
}

// waitObserver asserts the catalog observer fires (it runs on its own
// goroutine) within a generous window.
func waitObserver(t *testing.T, fired <-chan struct{}, context string) {
	t.Helper()
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: catalog observer did not fire", context)
	}
}

func TestRefreshServerTools_CapturesAnnotationsAndNotifies(t *testing.T) {
	m, _ := dynamicManager(t, "srv",
		`{"tools":[{"name":"ro_tool","description":"reads","annotations":{"readOnlyHint":true}},{"name":"rw_tool","description":"writes"}]}`,
	)
	fired := make(chan struct{}, 4)
	m.SetCatalogObserver(func() { fired <- struct{}{} })

	if _, _, err := m.RefreshServerTools("srv"); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	waitObserver(t, fired, "refresh with additions")

	m.mu.RLock()
	ro, rw := m.tools["ro_tool"], m.tools["rw_tool"]
	m.mu.RUnlock()
	if ro == nil || !ro.ReadOnlyHint() {
		t.Errorf("ro_tool must carry readOnlyHint=true through refresh, got %+v", ro)
	}
	if rw == nil || rw.ReadOnlyHint() {
		t.Errorf("rw_tool must not report read-only, got %+v", rw)
	}
}

func TestRefreshServerTools_NoChangesNoNotification(t *testing.T) {
	m, _ := dynamicManager(t, "srv",
		`{"tools":[{"name":"same","description":"unchanged"}]}`,
	)
	m.tools["same"] = &MCPTool{Name: "same", ServerName: "srv"}
	fired := make(chan struct{}, 1)
	m.SetCatalogObserver(func() { fired <- struct{}{} })

	if _, _, err := m.RefreshServerTools("srv"); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	select {
	case <-fired:
		t.Error("observer must not fire when the catalog is unchanged")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestMarkDisconnected_DropsToolsAndNotifies(t *testing.T) {
	m := NewManagerWithOptions(zap.NewNop(), ChannelManagerOptions{PersistDir: t.TempDir()})
	m.servers["srv"] = &ServerConnection{
		Config: ServerConfig{Name: "srv"},
		Status: ServerStatus{Connected: true},
	}
	m.tools["gone"] = &MCPTool{Name: "gone", ServerName: "srv"}
	fired := make(chan struct{}, 1)
	m.SetCatalogObserver(func() { fired <- struct{}{} })

	m.markDisconnected("srv", nil)
	waitObserver(t, fired, "disconnect dropping tools")
	if _, still := m.tools["gone"]; still {
		t.Error("disconnect must drop the server's tools")
	}
}
