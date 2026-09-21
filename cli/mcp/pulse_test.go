/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

// watchPulse turns the process-wide bus on for the test and returns a
// function that collects the MCP events seen so far.
func watchPulse(t *testing.T) func(n int) []pulse.Event {
	t.Helper()
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(256)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })
	var seen []pulse.Event
	return func(n int) []pulse.Event {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for len(seen) < n {
			select {
			case ev := <-ch:
				if ev.Kind == pulse.KindMCP {
					seen = append(seen, ev)
				}
			case <-deadline:
				t.Fatalf("got %d of %d MCP events: %+v", len(seen), n, seen)
			}
		}
		return seen
	}
}

func pulseTestManager(conn *ServerConnection, tools ...string) *Manager {
	m := NewManager(zap.NewNop())
	m.servers[conn.Config.Name] = conn
	for _, name := range tools {
		m.tools[name] = &MCPTool{Name: name, ServerName: conn.Config.Name}
	}
	return m
}

func TestCallToolReportsASpanWithOutcomeAndNoContent(t *testing.T) {
	collect := watchPulse(t)
	okResult, _ := json.Marshal(toolCallResult{Content: []toolContent{{Type: "text", Text: "SECRET-RESULT"}}})
	toolErr, _ := json.Marshal(toolCallResult{IsError: true, Content: []toolContent{{Type: "text", Text: "bad input"}}})
	conn := &ServerConnection{
		Config: ServerConfig{Name: "github", Transport: TransportStdio},
		transport: &mockTransport{calls: []mockCall{
			{method: "tools/call", result: okResult},
			{method: "tools/call", result: toolErr},
			{method: "tools/call", err: errors.New("pipe closed: SECRET-ERROR")},
		}},
	}
	m := pulseTestManager(conn)

	args := map[string]interface{}{"query": "SECRET-ARG"}
	if res, err := m.callTool(context.Background(), conn, "search_issues", args); err != nil || res.Content != "SECRET-RESULT" {
		t.Fatalf("call 1: %+v %v", res, err)
	}
	if res, err := m.callTool(context.Background(), conn, "search_issues", args); err != nil || !res.IsError {
		t.Fatalf("call 2 must surface the tool-level error: %+v %v", res, err)
	}
	if _, err := m.callTool(context.Background(), conn, "search_issues", args); err == nil {
		t.Fatal("call 3 must fail")
	}

	evs := collect(6)
	wantStatus := []string{pulse.StatusRunning, pulse.StatusOK, pulse.StatusRunning, pulse.StatusError, pulse.StatusRunning, pulse.StatusError}
	for i, ev := range evs {
		if ev.Name != "github" || ev.Status != wantStatus[i] {
			t.Errorf("event %d = %s/%s, want github/%s", i, ev.Name, ev.Status, wantStatus[i])
		}
	}
	if evs[1].ID != evs[0].ID || evs[1].Attrs["tool"] != "search_issues" || evs[1].Phase != pulse.PhaseEnd {
		t.Fatalf("end event = %+v", evs[1])
	}
	wire, _ := json.Marshal(evs)
	for _, secret := range []string{"SECRET-RESULT", "SECRET-ARG", "SECRET-ERROR", "bad input"} {
		if strings.Contains(string(wire), secret) {
			t.Fatalf("%q leaked onto the bus: %s", secret, wire)
		}
	}
}

func TestStartServerReportsLifecycle(t *testing.T) {
	collect := watchPulse(t)
	conn := &ServerConnection{Config: ServerConfig{Name: "broken", Transport: "carrier-pigeon"}}
	m := pulseTestManager(conn)
	if err := m.startServer(context.Background(), conn); err == nil {
		t.Fatal("unsupported transport must fail")
	}
	evs := collect(2)
	if evs[0].Attrs["state"] != pulseStateStarting || evs[0].Status != pulse.StatusRunning {
		t.Fatalf("first = %+v", evs[0])
	}
	if evs[1].Attrs["state"] != pulseStateFailed || evs[1].Status != pulse.StatusError || evs[1].Phase != pulse.PhaseUpdate {
		t.Fatalf("second = %+v", evs[1])
	}
	if evs[1].Attrs["transport"] != "carrier-pigeon" || evs[1].ID != "mcp:broken" {
		t.Fatalf("second = %+v", evs[1])
	}
}

func TestDisconnectAndStopAreReported(t *testing.T) {
	collect := watchPulse(t)
	conn := &ServerConnection{Config: ServerConfig{Name: "fs", Transport: TransportStdio}, Status: ServerStatus{Name: "fs", Connected: true}, transport: &mockTransport{}}
	m := pulseTestManager(conn, "mcp_fs_read")

	m.markDisconnected("fs", errors.New("EOF"))
	m.markDisconnected("fs", errors.New("EOF")) // already terminal: no second event
	if err := m.StopOne(context.Background(), "fs"); err != nil {
		t.Fatal(err)
	}
	evs := collect(2)
	if evs[0].Attrs["state"] != pulseStateDisconnected || evs[1].Attrs["state"] != pulseStateStopped {
		t.Fatalf("states = %q, %q", evs[0].Attrs["state"], evs[1].Attrs["state"])
	}
	if evs[1].Status != pulse.StatusOK {
		t.Fatalf("a deliberate stop is not an error: %+v", evs[1])
	}
}

func TestPulseSnapshotDescribesEveryServer(t *testing.T) {
	m := NewManager(zap.NewNop())
	m.servers["b-live"] = &ServerConnection{Config: ServerConfig{Name: "b-live", Transport: TransportSSE}, Status: ServerStatus{Connected: true}}
	m.servers["a-boot"] = &ServerConnection{Config: ServerConfig{Name: "a-boot"}, Status: ServerStatus{Starting: true}}
	m.servers["c-auth"] = &ServerConnection{Config: ServerConfig{Name: "c-auth"}, Status: ServerStatus{AuthRequired: true}}
	m.servers["d-dead"] = &ServerConnection{Config: ServerConfig{Name: "d-dead"}, Status: ServerStatus{LastError: errors.New("SECRET-ERROR")}}
	m.servers["e-idle"] = &ServerConnection{Config: ServerConfig{Name: "e-idle"}}
	m.tools["t1"] = &MCPTool{ServerName: "b-live"}
	m.tools["t2"] = &MCPTool{ServerName: "b-live"}

	got := m.PulseSnapshot()
	want := []struct{ name, state string }{
		{"a-boot", pulseStateStarting}, {"b-live", pulseStateConnected}, {"c-auth", pulseStateAuthRequired},
		{"d-dead", pulseStateFailed}, {"e-idle", pulseStateStopped},
	}
	if len(got) != len(want) {
		t.Fatalf("snapshot = %+v", got)
	}
	for i, w := range want {
		if got[i].Name != w.name || got[i].Attrs["state"] != w.state {
			t.Errorf("snapshot[%d] = %s/%s, want %s/%s", i, got[i].Name, got[i].Attrs["state"], w.name, w.state)
		}
	}
	if got[1].Attrs["tools"] != "2" || got[1].Attrs["transport"] != string(TransportSSE) {
		t.Fatalf("connected server = %+v", got[1])
	}
	wire, _ := json.Marshal(got)
	if strings.Contains(string(wire), "SECRET-ERROR") {
		t.Fatal("error text leaked into the snapshot")
	}
	var nilMgr *Manager
	if nilMgr.PulseSnapshot() != nil {
		t.Fatal("nil manager must snapshot as empty")
	}
}

func TestMCPTelemetryIsSilentWhileOff(t *testing.T) {
	conn := &ServerConnection{Config: ServerConfig{Name: "quiet", Transport: "nope"}}
	m := pulseTestManager(conn)
	before := pulse.Default().Stats().Published
	_ = m.startServer(context.Background(), conn)
	m.pulseState(nil, pulseStateStopped)
	if got := pulse.Default().Stats().Published; got != before {
		t.Fatalf("published %d events with the dashboard off", got-before)
	}
}
