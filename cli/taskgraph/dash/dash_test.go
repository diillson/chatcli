/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package dash

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/taskgraph"
)

func startTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	base := t.TempDir()
	g := &taskgraph.Graph{Name: "dash-run", Tasks: []*taskgraph.Task{
		{ID: "T1", Prompt: "p", Status: taskgraph.StatusDone},
		{ID: "T2", Prompt: "p2", Deps: []string{"T1"}, Status: taskgraph.StatusRunning},
	}}
	store, err := taskgraph.CreateRun(base, g)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, ev := range []taskgraph.Event{
		{Task: "T1", Type: taskgraph.EventTaskStarted},
		{Task: "T1", Type: taskgraph.EventTaskDone, Detail: "ok"},
	} {
		if err := store.AppendEvent(ev); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	s, err := Start(base)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return s, g.RunID
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) // #nosec G107 -- test URL from the local test server
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestDashServesIndexAndAPIs(t *testing.T) {
	s, runID := startTestServer(t)

	code, body := get(t, s.URL())
	if code != 200 || !strings.Contains(body, "Task Graph") || !strings.Contains(body, "<canvas") {
		t.Fatalf("index: %d %q", code, body[:min(120, len(body))])
	}

	code, body = get(t, s.URL()+"api/runs")
	if code != 200 || !strings.Contains(body, runID) {
		t.Fatalf("runs: %d %s", code, body)
	}

	// State: explicit run and default-latest must agree.
	for _, u := range []string{s.URL() + "api/state?run=" + runID, s.URL() + "api/state"} {
		code, body = get(t, u)
		if code != 200 {
			t.Fatalf("state %s: %d %s", u, code, body)
		}
		var g taskgraph.Graph
		if err := json.Unmarshal([]byte(body), &g); err != nil || len(g.Tasks) != 2 {
			t.Fatalf("state payload: %v %s", err, body)
		}
	}

	code, body = get(t, s.URL()+"api/events?run="+runID+"&since=0")
	var ev struct {
		Events []json.RawMessage `json:"events"`
		Next   int               `json:"next"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &ev) != nil || len(ev.Events) != 2 || ev.Next != 2 {
		t.Fatalf("events: %d %s", code, body)
	}
	// Incremental: since=next returns nothing new.
	code, body = get(t, s.URL()+"api/events?run="+runID+"&since=2")
	_ = json.Unmarshal([]byte(body), &ev)
	if code != 200 || len(ev.Events) != 0 {
		t.Fatalf("incremental events: %d %s", code, body)
	}
}

func TestDashRejectsUnknownRunAndPath(t *testing.T) {
	s, _ := startTestServer(t)
	if code, _ := get(t, s.URL()+"api/state?run=tg-nope"); code != 404 {
		t.Fatalf("unknown run: %d", code)
	}
	if code, _ := get(t, s.URL()+"api/state?run=../escape"); code != 404 {
		t.Fatalf("traversal run id: %d", code)
	}
	if code, _ := get(t, s.URL()+"nope"); code != 404 {
		t.Fatalf("unknown path: %d", code)
	}
}

func TestDashShutdownIdempotent(t *testing.T) {
	s, _ := startTestServer(t)
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}
