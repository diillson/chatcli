/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the MCP client. Each configured server is one node on
 * the dashboard: its lifecycle (starting, connected, failed, disconnected,
 * stopped) arrives as state updates on that node, and every tools/call is a
 * timed span on it. The MCP transports use their own http.Client, outside
 * the shared outbound funnel, so this is the only place they are visible.
 *
 * Only names and counts leave the process: server name, transport, tool
 * name, tool count. Call arguments, results and error text never do.
 */
package mcp

import (
	"sort"
	"strconv"

	"github.com/diillson/chatcli/pkg/pulse"
)

// MCP server states as shown on the live dashboard.
const (
	pulseStateStarting     = "starting"
	pulseStateConnected    = "connected"
	pulseStateFailed       = "failed"
	pulseStateDisconnected = "disconnected"
	pulseStateStopped      = "stopped"
	pulseStateAuthRequired = "auth required"
)

// pulseServerEvent builds the state update of one server.
func pulseServerEvent(name, transport, state string, tools int) pulse.Event {
	status := pulse.StatusOK
	switch state {
	case pulseStateStarting:
		status = pulse.StatusRunning
	case pulseStateFailed, pulseStateDisconnected, pulseStateAuthRequired:
		status = pulse.StatusError
	}
	ev := pulse.Event{
		Kind:   pulse.KindMCP,
		Phase:  pulse.PhaseUpdate,
		ID:     "mcp:" + name,
		Name:   name,
		Status: status,
	}
	ev = ev.With("state", state).With("transport", transport)
	if state == pulseStateConnected {
		ev = ev.With("tools", strconv.Itoa(tools))
	}
	return ev
}

// pulseState reports a lifecycle transition. Must be called without m.mu
// held: it may read the tool table.
func (m *Manager) pulseState(conn *ServerConnection, state string) {
	if !pulse.Enabled() || conn == nil {
		return
	}
	tools := 0
	if state == pulseStateConnected {
		m.mu.RLock()
		for _, t := range m.tools {
			if t.ServerName == conn.Config.Name {
				tools++
			}
		}
		m.mu.RUnlock()
	}
	pulse.Emit(pulseServerEvent(conn.Config.Name, string(conn.Config.Transport), state, tools))
}

// PulseSnapshot describes every configured server as it is right now, so a
// dashboard opened mid-session shows the servers already connected and not
// only the ones that change state afterwards.
func (m *Manager) PulseSnapshot() []pulse.Event {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	counts := make(map[string]int, len(names))
	for _, t := range m.tools {
		counts[t.ServerName]++
	}
	out := make([]pulse.Event, 0, len(names))
	for _, name := range names {
		conn := m.servers[name]
		state := pulseStateStopped
		switch {
		case conn.Status.Connected:
			state = pulseStateConnected
		case conn.Status.Starting:
			state = pulseStateStarting
		case conn.Status.AuthRequired:
			state = pulseStateAuthRequired
		case conn.Status.LastError != nil:
			state = pulseStateFailed
		}
		out = append(out, pulseServerEvent(name, string(conn.Config.Transport), state, counts[name]))
	}
	return out
}
