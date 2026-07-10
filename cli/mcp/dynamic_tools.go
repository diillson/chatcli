/*
 * ChatCLI - MCP Dynamic Tool Discovery
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Servers with dynamic tool lists (MCP capability tools.listChanged) start
 * with a minimal catalog — often a single bootstrap tool — and emit
 * "notifications/tools/list_changed" when it grows or shrinks (e.g. HTTP
 * Toolkit exposes only "start"; after it runs, the real tools appear).
 *
 * The ChannelManager fires SetToolsChangedHook on that notification; this
 * file owns what happens next: an async, per-server debounced re-run of
 * tools/list that reconciles the registry (add new, update changed, drop
 * vanished) and records the delta for the agent loop to surface to the
 * model at the next turn boundary.
 *
 * The refresh MUST be async relative to the hook: the hook runs on the
 * transport's reader goroutine, and a synchronous tools/list round-trip
 * from there would deadlock the response pump.
 */
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ToolListChange records one reconciled tools/list refresh, drained by the
// agent loop to tell the model which tools appeared or vanished mid-task.
type ToolListChange struct {
	Server  string
	Added   []string
	Removed []string
}

// dynamicToolRefreshDebounce coalesces notification bursts: servers often
// emit several list_changed events while registering tools one by one.
// Variable (not const) so tests can shrink the window.
var dynamicToolRefreshDebounce = 300 * time.Millisecond

// dynamicToolRefreshEnabled is the kill switch for the whole feature.
// Default on; CHATCLI_MCP_DYNAMIC_TOOLS=false|0|off|no disables, leaving
// the legacy behavior (notification lands in the channel inbox only).
func dynamicToolRefreshEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_MCP_DYNAMIC_TOOLS")))
	return v != "false" && v != "0" && v != "off" && v != "no"
}

// scheduleToolRefresh is the ChannelManager hook target. Non-blocking:
// marks the server pending and spawns the debounced refresh goroutine.
// Repeat notifications inside the debounce window coalesce into one run.
func (m *Manager) scheduleToolRefresh(serverName string) {
	if !dynamicToolRefreshEnabled() {
		m.logger.Debug("MCP dynamic tool refresh disabled, ignoring list_changed",
			zap.String("server", serverName))
		return
	}

	m.refreshMu.Lock()
	if m.refreshPending == nil {
		m.refreshPending = make(map[string]bool)
	}
	if m.refreshPending[serverName] {
		m.refreshMu.Unlock()
		return
	}
	m.refreshPending[serverName] = true
	m.refreshMu.Unlock()

	go func() {
		time.Sleep(dynamicToolRefreshDebounce)
		m.refreshMu.Lock()
		delete(m.refreshPending, serverName)
		m.refreshMu.Unlock()

		added, removed, err := m.RefreshServerTools(serverName)
		if err != nil {
			m.logger.Warn("MCP tool list refresh failed",
				zap.String("server", serverName), zap.Error(err))
			return
		}
		if len(added) == 0 && len(removed) == 0 {
			m.logger.Debug("MCP tool list unchanged after list_changed",
				zap.String("server", serverName))
			return
		}

		m.refreshMu.Lock()
		m.toolListChanges = append(m.toolListChanges, ToolListChange{
			Server:  serverName,
			Added:   added,
			Removed: removed,
		})
		m.refreshMu.Unlock()

		m.logger.Info("MCP tool list refreshed",
			zap.String("server", serverName),
			zap.Strings("added", added),
			zap.Strings("removed", removed))
	}()
}

// RefreshServerTools re-runs tools/list for one server and reconciles the
// registry: new tools are added, changed schemas/descriptions replaced,
// and tools the server no longer advertises are dropped (only entries
// owned by this server — a same-named tool from another server survives).
// Returns the sorted names of added and removed tools.
func (m *Manager) RefreshServerTools(serverName string) (added, removed []string, err error) {
	m.mu.RLock()
	conn, ok := m.servers[serverName]
	var transport mcpTransport
	if ok && conn != nil {
		transport = conn.transport
	}
	m.mu.RUnlock()
	if !ok || transport == nil {
		return nil, nil, fmt.Errorf("MCP server %q is not connected", serverName)
	}

	// The round-trip happens outside the registry lock: Call blocks until
	// the server answers, and tool execution paths need the read lock.
	result, err := transport.Call("tools/list", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("tools/list for %q: %w", serverName, err)
	}
	var toolsList toolsListResult
	if err := json.Unmarshal(result, &toolsList); err != nil {
		return nil, nil, fmt.Errorf("parsing tools/list for %q: %w", serverName, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	previous := make(map[string]struct{})
	for name, t := range m.tools {
		if t.ServerName == serverName {
			previous[name] = struct{}{}
		}
	}

	fresh := make(map[string]struct{}, len(toolsList.Tools))
	for _, t := range toolsList.Tools {
		fresh[t.Name] = struct{}{}
		if _, existed := previous[t.Name]; !existed {
			added = append(added, t.Name)
		}
		m.tools[t.Name] = &MCPTool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
			ServerName:  serverName,
		}
	}
	for name := range previous {
		if _, still := fresh[name]; still {
			continue
		}
		removed = append(removed, name)
		delete(m.tools, name)
	}
	conn.Status.ToolCount = len(toolsList.Tools)

	sort.Strings(added)
	sort.Strings(removed)
	return added, removed, nil
}

// DrainToolListChanges returns the reconciled refreshes accumulated since
// the last drain and clears the queue. The agent loop calls this at each
// turn boundary to inject a catalog-change notice for the model.
func (m *Manager) DrainToolListChanges() []ToolListChange {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	out := m.toolListChanges
	m.toolListChanges = nil
	return out
}
