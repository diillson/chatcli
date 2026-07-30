/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestServer_PostReplyResultOrdering asserts the response line is written
// before the Post callback's notification.
func TestServer_PostReplyResultOrdering(t *testing.T) {
	var out strings.Builder
	var srv *Server
	handler := func(ctx context.Context, method string, params json.RawMessage) (interface{}, *RPCError) {
		return PostReplyResult{
			Result: map[string]interface{}{"ok": true},
			Post:   func() { _ = srv.Notify("after/reply", map[string]interface{}{"n": 1}) },
		}, nil
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"do"}` + "\n")
	srv = NewServer(in, &syncWriter{w: &out}, handler)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"ok":true`) {
		t.Errorf("first line should be the response, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "after/reply") {
		t.Errorf("second line should be the notification, got %q", lines[1])
	}
}

// TestServer_RequestRoundTrip drives a server→client request end to end: the
// fake client reads the request off the wire and answers it.
func TestServer_RequestRoundTrip(t *testing.T) {
	clientIn, srvOut := io.Pipe() // server writes → client reads
	srvIn, clientOut := io.Pipe() // client writes → server reads
	srv := NewServer(srvIn, srvOut, func(ctx context.Context, m string, p json.RawMessage) (interface{}, *RPCError) {
		return nil, errf(CodeMethodNotFound, "none")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	// Fake client: answer the first request it sees.
	go func() {
		br := bufio.NewReader(clientIn)
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal([]byte(line), &req) != nil || req.Method != "session/request_permission" {
			return
		}
		resp, _ := json.Marshal(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]interface{}{"outcome": map[string]interface{}{"outcome": "selected", "optionId": "allow-once"}},
		})
		_, _ = clientOut.Write(append(resp, '\n'))
	}()

	raw, err := srv.Request(ctx, "session/request_permission", map[string]interface{}{"sessionId": "s1"})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if !strings.Contains(string(raw), "allow-once") {
		t.Errorf("unexpected result: %s", raw)
	}
	cancel()
	_ = clientOut.Close()
}

// TestServer_MalformedFrameStillErrors asserts a frame with an id but no
// method AND no result/error member is answered as an invalid request — the
// client-response routing must not swallow it silently.
func TestServer_MalformedFrameStillErrors(t *testing.T) {
	var out strings.Builder
	srv := NewServer(strings.NewReader(`{"jsonrpc":"2.0","id":7}`+"\n"), &syncWriter{w: &out},
		func(ctx context.Context, m string, p json.RawMessage) (interface{}, *RPCError) { return nil, nil })
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !strings.Contains(out.String(), "-32600") {
		t.Errorf("expected invalid-request error, got %q", out.String())
	}
}

// TestServer_EOFUnblocksPendingRequest asserts a handler blocked in
// Server.Request is released when the client closes stdin — the process must
// not wedge on wg.Wait forever.
func TestServer_EOFUnblocksPendingRequest(t *testing.T) {
	srvIn, clientOut := io.Pipe()
	var out strings.Builder
	var srv *Server
	handlerDone := make(chan error, 1)
	srv = NewServer(srvIn, &syncWriter{w: &out}, func(ctx context.Context, m string, p json.RawMessage) (interface{}, *RPCError) {
		_, err := srv.Request(ctx, "session/request_permission", map[string]interface{}{})
		handlerDone <- err
		return map[string]interface{}{}, nil
	})

	served := make(chan error, 1)
	go func() { served <- srv.Serve(context.Background()) }()

	_, _ = clientOut.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"do"}` + "\n"))
	time.Sleep(100 * time.Millisecond) // let the handler reach Request
	_ = clientOut.Close()              // client dies without answering

	select {
	case err := <-handlerDone:
		if err == nil {
			t.Error("pending request must fail on EOF")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler wedged: EOF must unblock pending Server.Request")
	}
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve must return after EOF")
	}
}

// TestServer_RequestCanceled asserts a pending request unblocks on ctx cancel.
func TestServer_RequestCanceled(t *testing.T) {
	var out strings.Builder
	srv := NewServer(strings.NewReader(""), &syncWriter{w: &out}, func(ctx context.Context, m string, p json.RawMessage) (interface{}, *RPCError) {
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	_, err := srv.Request(ctx, "session/request_permission", map[string]interface{}{})
	if err == nil {
		t.Fatal("expected error on canceled request")
	}
}

// syncWriter serializes writes from concurrent goroutines onto a Builder.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
