/*
 * ChatCLI - Tests for the Streamable HTTP MCP transport
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Each test spins a small httptest.Server that impersonates one of
 * the server-side shapes allowed by the 2025-03-26 spec, then drives
 * the transport via its public Call surface. The aim is to lock the
 * happy paths (JSON one-shot, SSE upgrade, 202 notification) plus the
 * edges that bit us in the wild — session header propagation, auth
 * injection, and timeout localization.
 */
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// newTestServerConfig builds the minimal ServerConfig the transport
// needs. Callers override URL after the test server is up.
func newTestServerConfig(url string) ServerConfig {
	return ServerConfig{
		Name:      "test",
		Transport: TransportStreamableHTTP,
		URL:       url,
		Enabled:   true,
	}
}

func newTestTransport(t *testing.T, srv *httptest.Server, mutate func(*ServerConfig)) *httpTransport {
	t.Helper()
	cfg := newTestServerConfig(srv.URL)
	if mutate != nil {
		mutate(&cfg)
	}
	tr, err := newHTTPTransport(context.Background(), cfg, zap.NewNop(), nil, "test")
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close(context.Background()) })
	return tr
}

func TestHTTPTransport_RequiresURL(t *testing.T) {
	cfg := ServerConfig{Name: "x", Transport: TransportStreamableHTTP}
	if _, err := newHTTPTransport(context.Background(), cfg, zap.NewNop(), nil, "x"); err == nil {
		t.Fatalf("expected error when URL is empty")
	}
}

func TestHTTPTransport_JSONOneShotResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.Header.Get("Accept"), "application/json") {
			t.Errorf("Accept missing application/json: %q", r.Header.Get("Accept"))
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.Errorf("Accept missing text/event-stream: %q", r.Header.Get("Accept"))
		}
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"hello":"world"}`),
		})
	}))
	defer srv.Close()

	tr := newTestTransport(t, srv, nil)
	got, err := tr.Call("ping", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(got) != `{"hello":"world"}` {
		t.Errorf("Result = %s, want {\"hello\":\"world\"}", string(got))
	}
}

func TestHTTPTransport_SSEUpgradeResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// A leading server-initiated notification (no ID) — should
		// be routed to channelMgr but ignored by the caller.
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", `{"jsonrpc":"2.0","method":"notify","params":{"x":1}}`)
		if flusher != nil {
			flusher.Flush()
		}
		// Then the actual response to our request.
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"ok":true}}`, req.ID))
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	tr := newTestTransport(t, srv, nil)
	got, err := tr.Call("tools/list", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Errorf("Result = %s, want {\"ok\":true}", string(got))
	}
}

func TestHTTPTransport_202NotificationReturnsNilResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	tr := newTestTransport(t, srv, nil)
	got, err := tr.Call("notifications/initialized", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got != nil {
		t.Errorf("Result = %s, want nil for 202 notification", string(got))
	}
}

func TestHTTPTransport_HTTPErrorStatusIsLocalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `forbidden`)
	}))
	defer srv.Close()

	tr := newTestTransport(t, srv, nil)
	_, err := tr.Call("tools/list", nil)
	if err == nil {
		t.Fatalf("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q should mention status 401", err.Error())
	}
}

func TestHTTPTransport_RPCErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: "Method not found"},
		})
	}))
	defer srv.Close()

	tr := newTestTransport(t, srv, nil)
	_, err := tr.Call("bogus", nil)
	if err == nil {
		t.Fatalf("expected JSON-RPC error")
	}
	if !strings.Contains(err.Error(), "Method not found") {
		t.Errorf("error %q should carry server message", err.Error())
	}
}

func TestHTTPTransport_SessionIDIsCapturedAndEchoed(t *testing.T) {
	var (
		mu          sync.Mutex
		seenSession []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenSession = append(seenSession, r.Header.Get("Mcp-Session-Id"))
		mu.Unlock()

		var req jsonRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		// First call (initialize) sets the session ID; subsequent
		// calls must echo it back.
		w.Header().Set("Mcp-Session-Id", "sess-42")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{}`),
		})
	}))
	defer srv.Close()

	tr := newTestTransport(t, srv, nil)
	if _, err := tr.Call("initialize", nil); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, err := tr.Call("tools/list", nil); err != nil {
		t.Fatalf("tools/list: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenSession) != 2 {
		t.Fatalf("seenSession len = %d, want 2", len(seenSession))
	}
	if seenSession[0] != "" {
		t.Errorf("initialize should NOT echo a session header (none known yet); got %q", seenSession[0])
	}
	if seenSession[1] != "sess-42" {
		t.Errorf("second call session header = %q, want sess-42", seenSession[1])
	}
}

func TestHTTPTransport_BearerAuthIsApplied(t *testing.T) {
	t.Setenv("MCP_TOKEN", "super-secret")
	var seenAuth atomic.Value
	seenAuth.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		var req jsonRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{}`)})
	}))
	defer srv.Close()

	tr := newTestTransport(t, srv, func(c *ServerConfig) {
		c.Auth = &AuthConfig{Type: "bearer", Token: "${MCP_TOKEN}"}
	})
	if _, err := tr.Call("ping", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := seenAuth.Load().(string); got != "Bearer super-secret" {
		t.Errorf("Authorization = %q, want Bearer super-secret", got)
	}
}

func TestHTTPTransport_CallTimeoutIsLocalized(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-block // hold the request open until the test closes it
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(block)

	tr := newTestTransport(t, srv, func(c *ServerConfig) {
		c.Timeout = 1 // 1 second; the handler never responds
	})
	start := time.Now()
	_, err := tr.Call("slow", nil)
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Call ran much longer than timeout (%s)", time.Since(start))
	}
	if !strings.Contains(err.Error(), "slow") {
		t.Errorf("timeout error %q should mention the method name", err.Error())
	}
}

func TestHTTPTransport_BadContentTypeRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>oops</html>")
	}))
	defer srv.Close()

	tr := newTestTransport(t, srv, nil)
	_, err := tr.Call("ping", nil)
	if err == nil {
		t.Fatalf("expected error on text/html response")
	}
	if !strings.Contains(err.Error(), "text/html") {
		t.Errorf("error %q should surface the offending Content-Type", err.Error())
	}
}

func TestHTTPTransport_CloseIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := newTestTransport(t, srv, nil)
	ctx := context.Background()
	if err := tr.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := tr.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := tr.Call("ping", nil); err == nil {
		t.Fatalf("expected Call after Close to error")
	}
}

func TestTransportStreamableHTTPConstant(t *testing.T) {
	if TransportStreamableHTTP != "http" {
		t.Errorf("TransportStreamableHTTP = %q, want \"http\"", TransportStreamableHTTP)
	}
}
