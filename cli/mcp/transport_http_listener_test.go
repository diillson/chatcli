/*
 * ChatCLI - Tests for the HTTP Streamable transport push listener
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the StartPushListener / pushSupervisor / pushRunOnce
 * surface added to enable server-initiated notifications on
 * Streamable HTTP transports.
 */
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newHTTPListenerTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// TestStartPushListener_ReceivesNotifications verifies the happy path:
// the GET listener opens, the server emits an SSE notification, and
// the channel manager records it.
func TestStartPushListener_ReceivesNotifications(t *testing.T) {
	srv := newHTTPListenerTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("response writer does not support flush")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/ci\",\"params\":{\"build\":\"42\"}}\n\n")
		flusher.Flush()
		<-r.Context().Done()
	})

	cm := NewChannelManager(zap.NewNop())
	cfg := ServerConfig{Name: "test", URL: srv.URL, Transport: TransportStreamableHTTP, Timeout: 5, InitTimeout: 5}
	tr, err := newHTTPTransport(context.Background(), cfg, zap.NewNop(), cm, "test")
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close(context.Background()) })

	tr.StartPushListener()
	// Listener is idempotent — second call must not block or panic.
	tr.StartPushListener()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cm.Count() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cm.Count() == 0 {
		t.Fatalf("expected at least 1 message in channel manager, got 0")
	}

	msgs := cm.GetRecent(10)
	if msgs[0].Channel != "ci" {
		t.Errorf("Channel = %q, want ci", msgs[0].Channel)
	}
}

// TestStartPushListener_StopsCleanlyOn405 verifies the spec-compliant
// "server does not support push" path: HTTP 405 stops the listener
// without retry storms.
func TestStartPushListener_StopsCleanlyOn405(t *testing.T) {
	var calls atomic.Int32
	srv := newHTTPListenerTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			calls.Add(1)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	cm := NewChannelManager(zap.NewNop())
	cfg := ServerConfig{Name: "test", URL: srv.URL, Transport: TransportStreamableHTTP, Timeout: 5, InitTimeout: 5}
	tr, err := newHTTPTransport(context.Background(), cfg, zap.NewNop(), cm, "test")
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close(context.Background()) })

	tr.StartPushListener()

	// Wait long enough for the supervisor to call GET once and exit.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-tr.pushDone:
			if got := calls.Load(); got != 1 {
				t.Errorf("expected exactly 1 GET attempt before clean stop, got %d", got)
			}
			return
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Fatalf("listener did not stop cleanly after 405; calls=%d", calls.Load())
}

// TestStartPushListener_NoOpAfterClose ensures the listener obeys
// the closed flag — calling StartPushListener on a closed transport
// must not start a supervisor goroutine.
func TestStartPushListener_NoOpAfterClose(t *testing.T) {
	srv := newHTTPListenerTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	cm := NewChannelManager(zap.NewNop())
	cfg := ServerConfig{Name: "test", URL: srv.URL, Transport: TransportStreamableHTTP, Timeout: 5, InitTimeout: 5}
	tr, err := newHTTPTransport(context.Background(), cfg, zap.NewNop(), cm, "test")
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}

	_ = tr.Close(context.Background())
	tr.StartPushListener() // must be no-op

	if tr.pushDone != nil {
		t.Errorf("StartPushListener after Close should not initialize pushDone")
	}
}

// TestHandlePushEvent_RoutesToChannelMgr exercises the event handler
// in isolation — fed an SSE event payload, it should land in the
// channel manager via ProcessSSENotification.
func TestHandlePushEvent_RoutesToChannelMgr(t *testing.T) {
	cm := NewChannelManager(zap.NewNop())
	tr := &httpTransport{
		channelMgr: cm,
		serverName: "test",
		logger:     zap.NewNop(),
	}
	tr.handlePushEvent("message", `{"jsonrpc":"2.0","method":"notifications/alerts","params":{"sev":"critical"}}`)

	if got := cm.Count(); got != 1 {
		t.Fatalf("Count = %d, want 1", got)
	}
	msgs := cm.GetRecent(1)
	if msgs[0].Channel != "alerts" {
		t.Errorf("Channel = %q, want alerts", msgs[0].Channel)
	}
}

// TestHandlePushEvent_NilChannelMgrIsNoop guards against the test
// path where a transport is exercised without a channel manager.
func TestHandlePushEvent_NilChannelMgrIsNoop(t *testing.T) {
	tr := &httpTransport{logger: zap.NewNop()}
	// Must not panic
	tr.handlePushEvent("message", `{"jsonrpc":"2.0","method":"x"}`)
}

// TestHandlePushEvent_EmptyDataIsNoop covers the early-return when
// SSE delivered an empty event.
func TestHandlePushEvent_EmptyDataIsNoop(t *testing.T) {
	cm := NewChannelManager(zap.NewNop())
	tr := &httpTransport{channelMgr: cm, serverName: "x", logger: zap.NewNop()}
	tr.handlePushEvent("ping", "")
	if got := cm.Count(); got != 0 {
		t.Errorf("Count after empty event = %d, want 0", got)
	}
}

// TestStartPushListener_AttachesSessionHeader verifies the listener
// forwards the captured Mcp-Session-Id header from prior interactions.
func TestStartPushListener_AttachesSessionHeader(t *testing.T) {
	var sawSession atomic.Bool
	const sid = "test-session-xyz"

	srv := newHTTPListenerTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if r.Header.Get("Mcp-Session-Id") == sid {
				sawSession.Store(true)
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	cm := NewChannelManager(zap.NewNop())
	cfg := ServerConfig{Name: "test", URL: srv.URL, Transport: TransportStreamableHTTP, Timeout: 5, InitTimeout: 5}
	tr, err := newHTTPTransport(context.Background(), cfg, zap.NewNop(), cm, "test")
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close(context.Background()) })

	// Simulate a session captured from a prior initialize response.
	tr.setSession(sid)

	tr.StartPushListener()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sawSession.Load() {
			break
		}
		select {
		case <-tr.pushDone:
			break
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !sawSession.Load() {
		t.Errorf("listener GET did not include Mcp-Session-Id header")
	}
}

// TestGetByChannel_FilterAndOrder exercises the GetByChannel path
// uncovered by the broader channel tests — confirms wildcard and
// chronological order.
func TestGetByChannel_FilterAndOrder(t *testing.T) {
	cm := NewChannelManager(zap.NewNop())
	cm.Push(ChannelMessage{ServerName: "s", Channel: "a", Content: "1"})
	cm.Push(ChannelMessage{ServerName: "s", Channel: "b", Content: "2"})
	cm.Push(ChannelMessage{ServerName: "s", Channel: "a", Content: "3"})

	onlyA := cm.GetByChannel("a", 10)
	if len(onlyA) != 2 {
		t.Fatalf("len(onlyA) = %d, want 2", len(onlyA))
	}
	if onlyA[0].Content != "1" || onlyA[1].Content != "3" {
		t.Errorf("chronological order broken: %+v", onlyA)
	}

	all := cm.GetByChannel("*", 10)
	if len(all) != 3 {
		t.Fatalf("wildcard returned %d, want 3", len(all))
	}

	if got := cm.GetByChannel("a", 0); got != nil {
		t.Errorf("n=0 should return nil, got %v", got)
	}
	if got := cm.GetByChannel("nonexistent", 5); len(got) != 0 {
		t.Errorf("nonexistent channel should return empty, got %v", got)
	}
}

// TestSseSupervisor_ReconnectsOnDrop wires up an SSE server that
// drops the stream once and then serves a normal endpoint event.
// Validates the supervisor reconnects rather than giving up.
func TestSseSupervisor_ReconnectsOnDrop(t *testing.T) {
	var attempts atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("flush not supported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		// Send the endpoint event first so the transport considers
		// itself ready on every connection. Use proper SSE field
		// formatting (with "<space>" after the colon).
		fmt.Fprint(w, "event: endpoint\n")
		fmt.Fprint(w, "data: /messages\n\n")
		flusher.Flush()
		// Give the client a moment to scan the event before any
		// disconnect — otherwise the body close races the parser.
		time.Sleep(50 * time.Millisecond)
		if n == 1 {
			return // drop, supervisor reconnects
		}
		<-r.Context().Done()
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cm := NewChannelManager(zap.NewNop())
	cfg := ServerConfig{
		// URL is the base — the transport appends "/sse" internally.
		Name: "test", URL: srv.URL, Transport: TransportSSE,
		Timeout: 5, InitTimeout: 5,
	}
	tr, err := newSSETransport(context.Background(), cfg, zap.NewNop(), cm, "test")
	if err != nil {
		t.Fatalf("newSSETransport: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = tr.Close(ctx)
	}()

	// Wait for at least the reconnect attempt. Backoff starts at
	// 500ms with full jitter, so ~1.5s is enough.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if attempts.Load() >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("supervisor did not reconnect after drop; attempts=%d", attempts.Load())
}

// TestSseTransport_CallReturnsNotReadyWhenEndpointMissing reaches
// the early-fail path in Call() when no endpoint event was ever
// received — exercises the "not ready" branch end-to-end.
func TestSseTransport_CallReturnsNotReadyWhenEndpointMissing(t *testing.T) {
	tr := &sseTransport{
		baseURL:     "http://localhost",
		pending:     make(map[int64]chan *jsonRPCResponse),
		logger:      zap.NewNop(),
		initTimeout: 100 * time.Millisecond,
		ctx:         context.Background(),
	}

	_, err := tr.Call("ping", nil)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("expected not-ready error, got %v", err)
	}
}
