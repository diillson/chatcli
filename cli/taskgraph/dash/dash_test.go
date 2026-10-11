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

// base is the server root, without the token query.
func base(s *Server) string { return "http://" + s.host + "/" }

// get calls the API the way the page does: exact Host, token in the header.
func get(t *testing.T, s *Server, url string) (int, string) {
	t.Helper()
	return getWith(t, url, s.token, "")
}

func getWith(t *testing.T, url, token, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set(tokenHeader, token)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G107 -- test URL from the local test server
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestDashServesIndexAndAPIs(t *testing.T) {
	s, runID := startTestServer(t)

	code, body := getWith(t, s.URL(), "", "")
	if code != 200 || !strings.Contains(body, "Task Graph") || !strings.Contains(body, "<canvas") {
		t.Fatalf("index: %d %q", code, body[:min(120, len(body))])
	}
	if strings.Contains(body, s.token) {
		t.Fatal("the page must not embed the token")
	}

	code, body = get(t, s, base(s)+"api/runs")
	if code != 200 || !strings.Contains(body, runID) {
		t.Fatalf("runs: %d %s", code, body)
	}

	// State: explicit run and default-latest must agree.
	for _, u := range []string{base(s) + "api/state?run=" + runID, base(s) + "api/state"} {
		code, body = get(t, s, u)
		if code != 200 {
			t.Fatalf("state %s: %d %s", u, code, body)
		}
		var g taskgraph.Graph
		if err := json.Unmarshal([]byte(body), &g); err != nil || len(g.Tasks) != 2 {
			t.Fatalf("state payload: %v %s", err, body)
		}
	}

	code, body = get(t, s, base(s)+"api/events?run="+runID+"&since=0")
	var ev struct {
		Events []json.RawMessage `json:"events"`
		Next   int               `json:"next"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &ev) != nil || len(ev.Events) != 2 || ev.Next != 2 {
		t.Fatalf("events: %d %s", code, body)
	}
	// Incremental: since=next returns nothing new.
	code, body = get(t, s, base(s)+"api/events?run="+runID+"&since=2")
	_ = json.Unmarshal([]byte(body), &ev)
	if code != 200 || len(ev.Events) != 0 {
		t.Fatalf("incremental events: %d %s", code, body)
	}
}

func TestDashRejectsUnknownRunAndPath(t *testing.T) {
	s, _ := startTestServer(t)
	if code, _ := get(t, s, base(s)+"api/state?run=tg-nope"); code != 404 {
		t.Fatalf("unknown run: %d", code)
	}
	if code, _ := get(t, s, base(s)+"api/state?run=../escape"); code != 404 {
		t.Fatalf("traversal run id: %d", code)
	}
	if code, _ := getWith(t, base(s)+"nope?t="+s.token, "", ""); code != 404 {
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

// The dashboard serves run state (task prompts, agent output): nothing is
// answered without the exact Host and the per-run token.
func TestDashRequiresHostAndToken(t *testing.T) {
	s, _ := startTestServer(t)

	// Page: token in ?t=, exact Host.
	if code, _ := getWith(t, base(s), "", ""); code != http.StatusForbidden {
		t.Errorf("index without token = %d, want 403", code)
	}
	if code, _ := getWith(t, base(s)+"?t=nope", "", ""); code != http.StatusForbidden {
		t.Errorf("index with a wrong token = %d, want 403", code)
	}
	if code, _ := getWith(t, s.URL(), "", "rebind.example:80"); code != http.StatusForbidden {
		t.Errorf("index with a wrong Host = %d, want 403", code)
	}

	// API: token in the header only, exact Host.
	for _, path := range []string{"api/runs", "api/state", "api/events"} {
		if code, _ := getWith(t, base(s)+path, "", ""); code != http.StatusForbidden {
			t.Errorf("%s without token = %d, want 403", path, code)
		}
		if code, _ := getWith(t, base(s)+path+"?t="+s.token, "", ""); code != http.StatusForbidden {
			t.Errorf("%s with the token in the query = %d, want 403", path, code)
		}
		if code, _ := getWith(t, base(s)+path, s.token, "rebind.example:80"); code != http.StatusForbidden {
			t.Errorf("%s with a wrong Host = %d, want 403", path, code)
		}
	}

	// Security headers on every answer, refusals included.
	resp, err := http.Get(base(s) + "api/runs") // #nosec G107 -- local test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for h, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}
}
