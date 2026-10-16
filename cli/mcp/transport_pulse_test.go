/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// Remote MCP servers are reached through metered clients: the SSE stream and
// every streamable-HTTP request show as connections to the server's host.
func TestRemoteMCPTransportsAreMetered(t *testing.T) {
	rec := pulsetest.Record(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: endpoint\ndata: /messages\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("/mcp", func(http.ResponseWriter, *http.Request) {})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cm := NewChannelManager(zap.NewNop())
	sse, err := newSSETransport(context.Background(), ServerConfig{Name: "s", URL: srv.URL, Transport: TransportSSE, Timeout: 5, InitTimeout: 5}, zap.NewNop(), cm, "s")
	if err != nil {
		t.Fatalf("newSSETransport: %v", err)
	}
	// The stream is open for the transport's life: it shows as a live
	// connection before it ends.
	rec.WaitFor(t, "sse stream", pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseStart))
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = sse.Close(closeCtx)

	h, err := newHTTPTransport(context.Background(), ServerConfig{Name: "h", URL: srv.URL + "/mcp", Transport: TransportStreamableHTTP, Timeout: 5, InitTimeout: 5}, zap.NewNop(), cm, "h")
	if err != nil {
		t.Fatalf("newHTTPTransport: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(context.Background()) })
	before := len(rec.Match(pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd)))
	resp, err := h.httpClient.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	rec.WaitFor(t, "http request", func(pulse.Event) bool {
		return len(rec.Match(pulsetest.Is(pulse.KindConn, "127.0.0.1", pulse.PhaseEnd))) > before
	})
}
