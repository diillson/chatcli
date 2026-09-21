/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

// Platform API paths embed bot tokens (Telegram: /bot<token>/sendMessage), so
// a platform request may only ever show its hostname; and the long-poll loop,
// which fires without pause while idle, must not show at all.
func TestPlatformRequestsShowHostOnlyAndSkipLongPoll(t *testing.T) {
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(64)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sendMessage") {
			w.WriteHeader(http.StatusTooManyRequests)
		}
	}))
	defer srv.Close()
	c := newLoggingClient(nil, zap.NewNop(), "telegram")
	for _, path := range []string{"/bot123:SECRET-TOKEN/getUpdates", "/bot123:SECRET-TOKEN/getMe", "/bot123:SECRET-TOKEN/sendMessage"} {
		resp, err := c.Get(srv.URL + path) // #nosec G107 -- local test server
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	var evs []pulse.Event
	deadline := time.After(3 * time.Second)
	for len(evs) < 4 {
		select {
		case ev := <-ch:
			if ev.Kind == pulse.KindConn {
				evs = append(evs, ev)
			}
		case <-deadline:
			t.Fatalf("got %d of 4 events: %+v", len(evs), evs)
		}
	}
	select {
	case ev := <-ch:
		if ev.Kind == pulse.KindConn {
			t.Fatalf("the long-poll request must not be reported: %+v", ev)
		}
	case <-time.After(80 * time.Millisecond):
	}
	if evs[1].Status != pulse.StatusOK || evs[1].Attrs["status"] != "200" || evs[1].Attrs["platform"] != "telegram" || evs[1].Name != "127.0.0.1" {
		t.Fatalf("getMe end = %+v", evs[1])
	}
	if evs[3].Status != pulse.StatusError || evs[3].Attrs["status"] != "429" {
		t.Fatalf("a 429 must read as an error: %+v", evs[3])
	}
	wire, _ := json.Marshal(evs)
	if strings.Contains(string(wire), "SECRET-TOKEN") || strings.Contains(string(wire), "sendMessage") {
		t.Fatalf("the path leaked onto the bus: %s", wire)
	}
}

func TestPlatformSpanHelpersAreNilSafe(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.test/x", nil)
	if beginPlatformSpan(req, "slack") != nil {
		t.Fatal("dashboard off: no span")
	}
	endPlatformSpan(nil, nil, nil)
	if beginPlatformSpan(nil, "slack") != nil {
		t.Fatal("nil request: no span")
	}
}
