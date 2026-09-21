/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

// connEnds turns the process-wide bus on and returns a collector of the
// connection end events seen so far.
func connEnds(t *testing.T) func(n int) []pulse.Event {
	t.Helper()
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(64)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })
	var seen []pulse.Event
	return func(n int) []pulse.Event {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for len(seen) < n {
			select {
			case ev := <-ch:
				if ev.Kind == pulse.KindConn && ev.Phase == pulse.PhaseEnd {
					seen = append(seen, ev)
				}
			case <-deadline:
				t.Fatalf("got %d of %d connection end events: %+v", len(seen), n, seen)
			}
		}
		return seen
	}
}

// The OTLP exporter pushes to the user's collector on a timer, from a
// goroutine of its own. On the live dashboard each push is a connection.
func TestOTLPPushShowsAsAConnection(t *testing.T) {
	collect := connEnds(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	t.Setenv(EnvEndpoint, srv.URL)
	t.Setenv(EnvMetricsEndpoint, "")
	t.Setenv(EnvHeaders, "Authorization=Bearer%20SECRET-TOKEN")

	exp := NewFromEnv(func() []Metric { return []Metric{{Name: "chatcli.llm.tokens"}} }, nil)
	if exp == nil {
		t.Fatal("exporter must be enabled")
	}
	if err := exp.Push(context.Background()); err != nil {
		t.Fatalf("Push: %v", err)
	}
	end := collect(1)[0]
	if end.Name != "127.0.0.1" || end.Attrs["status"] != "200" || end.Attrs["method"] != "POST" || end.Attrs["req_bytes"] == "" {
		t.Fatalf("end = %+v", end)
	}
	for _, v := range end.Attrs {
		if v == "Bearer SECRET-TOKEN" || v == "/v1/metrics" {
			t.Fatalf("a header or the path leaked: %+v", end.Attrs)
		}
	}
}
