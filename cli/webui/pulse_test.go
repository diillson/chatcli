/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// parentBackend records the dashboard parent a chat turn ran under.
type parentBackend struct {
	*fakeBackend
	mu     sync.Mutex
	parent string
}

func (p *parentBackend) Chat(ctx context.Context, session, text string, o TurnOptions, sink cli.ChunkSink) (string, error) {
	p.mu.Lock()
	p.parent = pulse.ParentFrom(ctx)
	p.mu.Unlock()
	return p.fakeBackend.Chat(ctx, session, text, o, sink)
}

func TestPulseRouteNameIsTheTemplate(t *testing.T) {
	if got := pulseRouteName(http.MethodPost, "tools", 1); got != "web:POST tools/{1}" {
		t.Fatalf("name = %q", got)
	}
	if got := pulseRouteName(http.MethodGet, "status", 0); got != "web:GET status" {
		t.Fatalf("name = %q", got)
	}
}

// Each API request is one node named by its route template; the turn runs
// under it; the raw path, the tool name and the message never reach the bus.
func TestAPIRoutesShowByTemplateAndTurnsNestUnderThem(t *testing.T) {
	rec := pulsetest.Record(t)
	pb := &parentBackend{fakeBackend: newFakeBackend()}
	srv, err := Start(Options{Backend: pb, PermissionTimeout: 2 * time.Second, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	if code, _ := call(t, srv, http.MethodPost, "/api/tools/SECRET-TOOL", map[string]string{"args": "SECRET-ARGS"}, true); code == 0 {
		t.Fatal("no response")
	}
	if code, _ := call(t, srv, http.MethodGet, "/api/SECRET-PATH/x/y", nil, true); code != http.StatusNotFound {
		t.Fatalf("unknown route = %d", code)
	}
	b, _ := json.Marshal(map[string]string{"mode": "chat", "text": "SECRET-TEXT"})
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Host()+"/api/turn", bytes.NewReader(b))
	req.Header.Set(tokenHeader, srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("the turn must still stream through the status recorder: %s", resp.Header.Get("Content-Type"))
	}
	readSSE(t, resp)

	tool := rec.WaitFor(t, "tool route", pulsetest.Is(pulse.KindRPC, "web:POST tools/{1}", pulse.PhaseEnd))
	if tool.Attrs["method"] != http.MethodPost || tool.Attrs["status"] == "" {
		t.Fatalf("tool route = %+v", tool)
	}
	miss := rec.WaitFor(t, "unmatched", pulsetest.Is(pulse.KindRPC, "web:unmatched", pulse.PhaseEnd))
	if miss.Status != pulse.StatusError || miss.Attrs["status"] != "404" || miss.Attrs["method"] != "" {
		t.Fatalf("unmatched = %+v", miss)
	}
	turnStart := rec.WaitFor(t, "turn start", pulsetest.Is(pulse.KindRPC, "web:POST turn", pulse.PhaseStart))
	turnEnd := rec.WaitFor(t, "turn end", pulsetest.Is(pulse.KindRPC, "web:POST turn", pulse.PhaseEnd))
	if turnEnd.Status != pulse.StatusOK || turnEnd.Attrs["status"] != "200" {
		t.Fatalf("turn = %+v", turnEnd)
	}
	pb.mu.Lock()
	parent := pb.parent
	pb.mu.Unlock()
	if parent != turnStart.ID {
		t.Fatalf("chat ran under %q, want the route node %q", parent, turnStart.ID)
	}
	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("path, tool or text leaked onto the bus: %s", wire)
	}
}

func TestPulseEndRouteMapsStatus(t *testing.T) {
	rec := pulsetest.Record(t)
	for code, want := range map[int]string{200: pulse.StatusOK, 409: pulse.StatusBlocked, 400: pulse.StatusError, 500: pulse.StatusError} {
		_, span := pulseBeginRoute(context.Background(), http.MethodGet, "web:map")
		pulseEndRoute(span, code)
		var got string
		rec.WaitFor(t, "end", func(ev pulse.Event) bool {
			if ev.Name == "web:map" && ev.Phase == pulse.PhaseEnd && ev.ID == span.ID() {
				got = ev.Status
				return true
			}
			return false
		})
		if got != want {
			t.Errorf("code %d = %s, want %s", code, got, want)
		}
	}
	pulseEndRoute(nil, 200)
}
