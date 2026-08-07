/*
 * ChatCLI - MCP manager OAuth tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

// newFakeMCPServer answers initialize + tools/list with a single tool so a
// manager can bring the connection up without a real MCP server.
func newFakeMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result json.RawMessage
		switch req.Method {
		case "initialize":
			result = json.RawMessage(`{"protocolVersion":"2024-11-05","serverInfo":{"name":"fake"}}`)
		case "tools/list":
			result = json.RawMessage(`{"tools":[{"name":"ping","description":"p","inputSchema":{}}]}`)
		default:
			result = json.RawMessage(`{}`)
		}
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	}))
}

func managerWithServer(t *testing.T, cfg ServerConfig) *Manager {
	t.Helper()
	m := NewManagerWithOptions(zap.NewNop(), ChannelManagerOptions{})
	m.servers[cfg.Name] = &ServerConnection{
		Config: cfg,
		Status: ServerStatus{Name: cfg.Name},
		logs:   newLogRing(mcpLogRingCapacity),
	}
	return m
}

// TestLogoutOAuthStopsConnectedServer is the regression guard for the reported
// bug: logging out must stop the running server so its in-memory token is no
// longer used and its tools disappear — not merely delete the stored profile.
func TestLogoutOAuthStopsConnectedServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := newFakeMCPServer(t)
	defer srv.Close()

	m := managerWithServer(t, ServerConfig{
		Name: "aws", Transport: TransportStreamableHTTP, URL: srv.URL, Enabled: true,
	})
	ctx := context.Background()
	if err := m.StartOne(ctx, "aws"); err != nil {
		t.Fatalf("StartOne: %v", err)
	}
	if m.ToolCount() == 0 {
		t.Fatalf("expected tools after connect")
	}

	if err := m.LogoutOAuth(ctx, "aws"); err != nil {
		t.Fatalf("LogoutOAuth: %v", err)
	}

	for _, s := range m.GetServerStatus() {
		if s.Name == "aws" && s.Connected {
			t.Fatalf("server should be stopped after logout")
		}
	}
	if m.ToolCount() != 0 {
		t.Fatalf("tools should be gone after logout, got %d", m.ToolCount())
	}
}

// TestReconnectUsesLongLivedContext is the regression guard for the "context
// canceled" bug: a server reconnected after login must be tied to the session
// context, so cancelling the short-lived login context does not kill the
// freshly-connected transport.
func TestReconnectUsesLongLivedContext(t *testing.T) {
	srv := newFakeMCPServer(t)
	defer srv.Close()

	m := managerWithServer(t, ServerConfig{
		Name: "aws", Transport: TransportStreamableHTTP, URL: srv.URL, Enabled: true,
	})

	// Mimic LoginOAuth's reconnect: a bounded login context, detached via
	// WithoutCancel so the transport survives the login call returning.
	loginCtx, cancel := context.WithCancel(context.Background())
	if err := m.StartOne(context.WithoutCancel(loginCtx), "aws"); err != nil {
		t.Fatalf("StartOne: %v", err)
	}
	cancel() // login tool returns → its context is cancelled

	// A tool call must still succeed — the transport was detached.
	if _, err := m.ExecuteTool(context.Background(), "ping", nil); err != nil {
		t.Fatalf("tool call after login-context cancel should succeed, got: %v", err)
	}
}

func TestLogoutOAuthUnknownServer(t *testing.T) {
	m := NewManagerWithOptions(zap.NewNop(), ChannelManagerOptions{})
	if err := m.LogoutOAuth(context.Background(), "ghost"); err == nil {
		t.Fatalf("expected error for unknown server")
	}
}

func TestLoginOAuthRejectsNonRemoteAndNoURL(t *testing.T) {
	// stdio server → OAuth login does not apply.
	m := managerWithServer(t, ServerConfig{Name: "local", Transport: TransportStdio, Command: "echo"})
	if err := m.LoginOAuth(context.Background(), "local"); err == nil {
		t.Errorf("expected not-remote error for stdio server")
	}
	// http server without a URL → error before any network work.
	m2 := managerWithServer(t, ServerConfig{Name: "web", Transport: TransportStreamableHTTP})
	if err := m2.LoginOAuth(context.Background(), "web"); err == nil {
		t.Errorf("expected no-url error")
	}
	// unknown server.
	if err := m2.LoginOAuth(context.Background(), "ghost"); err == nil {
		t.Errorf("expected unknown-server error")
	}
}

func TestAuthRequiredServersAndHasCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := managerWithServer(t, ServerConfig{Name: "aws", Transport: TransportStreamableHTTP, URL: "https://x/mcp"})
	// Nothing flagged yet.
	if len(m.AuthRequiredServers()) != 0 {
		t.Fatalf("expected no auth-required servers")
	}
	// Flag it and confirm it surfaces.
	m.mu.Lock()
	m.servers["aws"].Status.AuthRequired = true
	m.mu.Unlock()
	got := m.AuthRequiredServers()
	if len(got) != 1 || got[0] != "aws" {
		t.Fatalf("AuthRequiredServers = %v, want [aws]", got)
	}
	// No credential stored in the fresh HOME.
	if m.HasOAuthCredential("aws") {
		t.Fatalf("expected no stored credential")
	}
}

// oauthFailTransport always fails Call with the configured error. Stands in
// for a live transport whose stored refresh token died mid-session.
type oauthFailTransport struct{ err error }

func (f *oauthFailTransport) Call(string, interface{}) (json.RawMessage, error) { return nil, f.err }
func (f *oauthFailTransport) Close(context.Context) error                       { return nil }

// TestCallToolMarksAuthRequiredOnOAuthError guards the mid-session path: when
// a tool call fails because the OAuth refresh token is dead, the server must
// be flagged AuthRequired so /mcp status and the agent's auth note offer the
// login hint — previously only the startup handshake ever set the flag.
func TestCallToolMarksAuthRequiredOnOAuthError(t *testing.T) {
	m := managerWithServer(t, ServerConfig{
		Name: "aws", Transport: TransportStreamableHTTP, URL: "http://unused", Enabled: true,
	})
	conn := m.servers["aws"]
	conn.Status.Connected = true
	conn.transport = &oauthFailTransport{err: &OAuthRequiredError{Server: "aws"}}

	_, err := m.callTool(context.Background(), conn, "ping", nil)
	if _, ok := IsOAuthRequired(err); !ok {
		t.Fatalf("callTool error = %v, want *OAuthRequiredError", err)
	}
	if !conn.Status.AuthRequired {
		t.Error("server must be flagged AuthRequired after a mid-session OAuth failure")
	}
	if got := m.AuthRequiredServers(); len(got) != 1 || got[0] != "aws" {
		t.Errorf("AuthRequiredServers() = %v, want [aws]", got)
	}
}
