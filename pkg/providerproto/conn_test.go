/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerproto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pair connects two Conns through in-memory pipes and runs both.
func pair(t *testing.T, a, b Handler) (*Conn, *Conn) {
	t.Helper()
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	ca := NewConn(ar, aw, a, Options{})
	cb := NewConn(br, bw, b, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = ca.Run(ctx) }()
	go func() { _ = cb.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = aw.Close()
		_ = bw.Close()
		<-ca.Done()
		<-cb.Done()
	})
	return ca, cb
}

func echoHandler(_ context.Context, req *Request) (json.RawMessage, error) {
	switch req.Method {
	case "echo":
		return req.Params, nil
	case "fail":
		return nil, NewError(ErrCodeRateLimited, "slow down")
	case "boom":
		panic("kaboom")
	case "plain":
		return nil, errors.New("plain failure")
	}
	return nil, NewError(ErrCodeMethodNotFound, fmt.Sprintf("unknown %s", req.Method))
}

func TestCallRoundTrip(t *testing.T) {
	host, _ := pair(t, nil, echoHandler)
	var out map[string]any
	if err := host.Call(context.Background(), "echo", map[string]any{"a": "b"}, &out); err != nil {
		t.Fatal(err)
	}
	if out["a"] != "b" {
		t.Fatalf("out = %v", out)
	}
}

func TestCallErrorsKeepCodes(t *testing.T) {
	host, _ := pair(t, nil, echoHandler)
	cases := map[string]int{
		"fail":    ErrCodeRateLimited,
		"boom":    ErrCodeInternal,
		"plain":   ErrCodeInternal,
		"missing": ErrCodeMethodNotFound,
	}
	for method, code := range cases {
		err := host.Call(context.Background(), method, nil, nil)
		var perr *Error
		if !errors.As(err, &perr) || perr.Code != code {
			t.Errorf("%s: err = %v, want code %d", method, err, code)
		}
	}
	err := host.Call(context.Background(), "fail", nil, nil)
	var perr *Error
	if errors.As(err, &perr) && !perr.Retryable() {
		t.Error("rate limited must be retryable")
	}
}

func TestPanicDoesNotKillTheSession(t *testing.T) {
	host, _ := pair(t, nil, echoHandler)
	_ = host.Call(context.Background(), "boom", nil, nil)
	var out map[string]any
	if err := host.Call(context.Background(), "echo", map[string]any{"still": "alive"}, &out); err != nil {
		t.Fatalf("session died after a handler panic: %v", err)
	}
}

func TestCancelPropagatesToPeer(t *testing.T) {
	started := make(chan struct{})
	observed := make(chan error, 1)
	plugin := func(ctx context.Context, req *Request) (json.RawMessage, error) {
		if req.Method != "slow" {
			return nil, nil
		}
		close(started)
		<-ctx.Done()
		observed <- ctx.Err()
		return nil, ctx.Err()
	}
	host, _ := pair(t, nil, plugin)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- host.Call(ctx, "slow", nil, nil) }()
	<-started
	cancel()

	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Call err = %v, want context.Canceled", err)
	}
	select {
	case err := <-observed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler ctx err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("$/cancelRequest never reached the handler")
	}
}

func TestNotificationsAndRequestIDInContext(t *testing.T) {
	got := make(chan string, 1)
	var id atomic.Value
	plugin := func(ctx context.Context, req *Request) (json.RawMessage, error) {
		switch req.Method {
		case "note":
			if !req.IsNotification() {
				t.Error("note should be a notification")
			}
			got <- string(req.Params)
		case "whoami":
			id.Store(string(RequestIDFromContext(ctx)))
		}
		return nil, nil
	}
	host, _ := pair(t, nil, plugin)
	if err := host.Notify(context.Background(), "note", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if p != `{"n":1}` {
			t.Fatalf("params = %s", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification not delivered")
	}
	if err := host.Call(context.Background(), "whoami", nil, nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := id.Load().(string); v == "" {
		t.Fatal("handler did not see its request id")
	}
}

func TestPendingCallsFailWhenPeerGoesAway(t *testing.T) {
	ar, _ := io.Pipe()
	_, aw := io.Pipe()
	c := NewConn(ar, aw, nil, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "never", nil, nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	_ = ar.Close()

	select {
	case err := <-errc:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("err = %v, want ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending call hung after shutdown")
	}
	<-done
	if err := c.Call(context.Background(), "after", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after close = %v, want ErrClosed", err)
	}
}

func TestMalformedInputIsReportedAndSkipped(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader("not json\n" + `{"jsonrpc":"1.0","id":1,"method":"x"}` + "\n")
	var protoErrs atomic.Int32
	c := NewConn(in, &out, echoHandler, Options{OnProtocolError: func(error) { protoErrs.Add(1) }})
	if err := c.Run(context.Background()); err != nil {
		t.Fatalf("Run = %v, want clean EOF", err)
	}
	if protoErrs.Load() != 2 {
		t.Fatalf("protocol errors = %d, want 2", protoErrs.Load())
	}
	resp := out.String()
	if !strings.Contains(resp, `"code":-32700`) || !strings.Contains(resp, `"code":-32600`) {
		t.Fatalf("responses = %s", resp)
	}
}

func TestOversizedMessageStopsTheConnection(t *testing.T) {
	big := strings.Repeat("a", MaxMessageBytes+10)
	c := NewConn(strings.NewReader(big), io.Discard, nil, Options{})
	if err := c.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Run = %v, want size error", err)
	}
}
