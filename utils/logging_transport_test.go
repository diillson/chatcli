/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// A server-sent-events response must reach the caller incrementally: the
// first chunk has to be readable while the server is still holding the rest
// of the stream open. Buffering the whole body in the transport turns a live
// token stream into one delayed blob and keeps it all in memory.
func TestLoggingTransport_StreamsEventStreamBodies(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: second\n\n")
	}))
	defer srv.Close()
	defer close(release)

	c := NewHTTPClient(zap.NewNop(), 10*time.Second)

	type result struct {
		line string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := c.Get(srv.URL) // #nosec G107 -- URL of the local test server
		if err != nil {
			got <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		got <- result{line: line, err: err}
	}()

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("reading first chunk: %v", r.err)
		}
		if strings.TrimSpace(r.line) != "data: first" {
			t.Fatalf("first chunk = %q, want %q", r.line, "data: first")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first SSE chunk was not delivered while the stream was still open: the transport buffered the body")
	}
}

// Non-streaming bodies keep the existing contract: the transport captures
// them for the sanitized debug log and hands the caller an intact, re-readable
// copy.
func TestLoggingTransport_BufferedBodyStaysIntactAndLogged(t *testing.T) {
	const payload = `{"ok":true,"api_key":"sk-should-never-be-logged-0000"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer srv.Close()

	core, logs := observer.New(zapcore.DebugLevel)
	c := NewHTTPClient(zap.New(core), 10*time.Second)

	resp, err := c.Get(srv.URL) // #nosec G107 -- URL of the local test server
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != payload {
		t.Fatalf("caller body = %q, want the untouched payload", body)
	}

	entries := logs.FilterMessage("Corpo da Resposta").All()
	if len(entries) != 1 {
		t.Fatalf("response body debug entries = %d, want 1", len(entries))
	}
	for _, f := range entries[0].Context {
		if strings.Contains(string(f.Interface.([]byte)), "sk-should-never-be-logged") {
			t.Fatal("response body log leaked a sensitive value")
		}
	}
}

// A streamed body is never captured, and the log says so instead of going
// silent, so a post-mortem reader knows why the body entry is missing.
func TestLoggingTransport_EventStreamIsNotCaptured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: only\n\n")
	}))
	defer srv.Close()

	core, logs := observer.New(zapcore.DebugLevel)
	c := NewHTTPClient(zap.New(core), 10*time.Second)

	resp, err := c.Get(srv.URL) // #nosec G107 -- URL of the local test server
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "data: only\n\n" {
		t.Fatalf("body = %q, err = %v", body, err)
	}
	if n := logs.FilterMessage("Corpo da Resposta").Len(); n != 0 {
		t.Fatalf("streamed body was captured %d time(s), want 0", n)
	}
	if n := logs.FilterMessage("Corpo da Resposta em streaming: não capturado").Len(); n != 1 {
		t.Fatalf("streaming notice entries = %d, want 1", n)
	}
}

func TestIsEventStream(t *testing.T) {
	cases := map[string]bool{
		"text/event-stream":                 true,
		"text/event-stream; charset=utf-8":  true,
		"TEXT/Event-Stream":                 true,
		"  text/event-stream ;charset=utf8": true,
		"application/json":                  false,
		"text/event-streaming":              false,
		"application/x-ndjson":              false,
		"":                                  false,
	}
	for in, want := range cases {
		if got := isEventStream(in); got != want {
			t.Errorf("isEventStream(%q) = %v, want %v", in, got, want)
		}
	}
}
