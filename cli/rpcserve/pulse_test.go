/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rpcserve

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// Each distinct method name becomes a node and the name comes off the wire:
// a client must not be able to flood the graph or smuggle text onto it.
func TestPulseMethodNameOnlyAcceptsProtocolShapedNames(t *testing.T) {
	for in, want := range map[string]string{
		"tools/call":                             "tools/call",
		"session/request_permission":             "session/request_permission",
		"$/cancelRequest":                        "$/cancelRequest",
		"notifications/initialized":              "notifications/initialized",
		"":                                       "other",
		"tools/call?key=SECRET":                  "other",
		"has space":                              "other",
		"<script>":                               "other",
		strings.Repeat("a", pulseMethodMaxLen+1): "other",
	} {
		if got := pulseMethodName(in); got != want {
			t.Errorf("pulseMethodName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInboundRequestsAreReportedByMethodOnly(t *testing.T) {
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(64)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(""), &out, func(_ context.Context, method string, _ json.RawMessage) (interface{}, *RPCError) {
		if method == "tools/call" {
			return nil, errf(CodeInternalError, "SECRET-ERROR")
		}
		return map[string]string{"answer": "SECRET-RESULT"}, nil
	})
	srv.dispatch(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"token":"SECRET-PARAM"}}`))
	srv.dispatch(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"SECRET-TOOL"}}`))
	srv.dispatch(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	srv.dispatch(context.Background(), []byte(`not json`)) // a parse error never reaches the handler: nothing to report

	var evs []pulse.Event
	deadline := time.After(3 * time.Second)
	for len(evs) < 6 {
		select {
		case ev := <-ch:
			if ev.Kind == pulse.KindRPC {
				evs = append(evs, ev)
			}
		case <-deadline:
			t.Fatalf("got %d of 6 events: %+v", len(evs), evs)
		}
	}
	want := []struct{ name, status string }{
		{"initialize", pulse.StatusOK}, {"tools/call", pulse.StatusError}, {"notifications/initialized", pulse.StatusOK},
	}
	for i, w := range want {
		start, end := evs[2*i], evs[2*i+1]
		if start.Name != w.name || end.Status != w.status || end.Attrs["direction"] != "inbound" || end.ID != start.ID {
			t.Errorf("request %d = %+v / %+v, want %s/%s", i, start, end, w.name, w.status)
		}
	}
	wire, _ := json.Marshal(evs)
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("params, result or error text leaked: %s", wire)
	}
	if !strings.Contains(out.String(), "SECRET-RESULT") {
		t.Fatal("the protocol response itself must be untouched")
	}
}

// A permission dialog nobody answers is a server request that never ends.
func TestOutboundRequestIsReportedUntilItResolves(t *testing.T) {
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(64)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	var out bytes.Buffer
	srv := NewServer(strings.NewReader(""), &out, func(context.Context, string, json.RawMessage) (interface{}, *RPCError) { return nil, nil })
	ctx, stop := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer stop()
	if _, err := srv.Request(ctx, "session/request_permission", map[string]string{"tool": "SECRET"}); err == nil {
		t.Fatal("nobody answers: the request must time out")
	}

	var evs []pulse.Event
	deadline := time.After(3 * time.Second)
	for len(evs) < 2 {
		select {
		case ev := <-ch:
			if ev.Kind == pulse.KindRPC {
				evs = append(evs, ev)
			}
		case <-deadline:
			t.Fatalf("got %d of 2 events", len(evs))
		}
	}
	if evs[0].Name != "client:session/request_permission" || evs[1].Status != pulse.StatusCancelled || evs[1].Attrs["direction"] != "outbound" {
		t.Fatalf("events = %+v", evs)
	}
	if evs[1].DurMS < 60 {
		t.Fatalf("the span must cover the wait: %dms", evs[1].DurMS)
	}
}

func TestRPCTelemetryIsSilentWhileOff(t *testing.T) {
	ctx := context.Background()
	got, span := pulseInbound(ctx, "x")
	if span != nil || got != ctx || pulseOutbound(ctx, "x") != nil {
		t.Fatal("dashboard off: no spans and the context untouched")
	}
	pulseEndRPC(nil, nil)
	pulseEndRPC(nil, errf(CodeInternalError, "x"))
}

// The handler runs under the request's node, so a turn or a tool the request
// starts hangs from the RPC instead of floating at the session, and a dialog
// the handler opens toward the client hangs from it too.
func TestInboundHandlerRunsUnderTheRequestNode(t *testing.T) {
	rec := pulsetest.Record(t)

	var out bytes.Buffer
	var handlerParent string
	var srv *Server
	srv = NewServer(strings.NewReader(""), &out, func(ctx context.Context, _ string, _ json.RawMessage) (interface{}, *RPCError) {
		handlerParent = pulse.ParentFrom(ctx)
		cctx, stop := context.WithTimeout(ctx, 10*time.Millisecond)
		defer stop()
		_, _ = srv.Request(cctx, "session/request_permission", nil)
		return nil, nil
	})
	srv.dispatch(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"session/prompt"}`))

	start := rec.WaitFor(t, "inbound start", pulsetest.Is(pulse.KindRPC, "session/prompt", pulse.PhaseStart))
	if handlerParent == "" || handlerParent != start.ID {
		t.Fatalf("handler parent = %q, want the RPC span %q", handlerParent, start.ID)
	}
	dialog := rec.WaitFor(t, "outbound start", pulsetest.Is(pulse.KindRPC, "client:session/request_permission", pulse.PhaseStart))
	if dialog.Parent != start.ID {
		t.Fatalf("outbound parent = %q, want %q", dialog.Parent, start.ID)
	}
}
