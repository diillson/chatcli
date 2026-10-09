/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli"
)

// controlsBackend is the fake engine with the switches: MCP servers it can
// start and stop, skills it can pin, and a completer that echoes the line.
type controlsBackend struct {
	*fakeBackend
	cmu      sync.Mutex
	running  map[string]bool
	pinned   map[string]bool
	manual   []string
	lastLine string
}

func newControlsBackend() *controlsBackend {
	return &controlsBackend{fakeBackend: newFakeBackend(), running: map[string]bool{"fs": true, "git": false}, pinned: map[string]bool{}, manual: []string{"deploy"}}
}

func (c *controlsBackend) Status() Status {
	st := c.fakeBackend.Status()
	c.cmu.Lock()
	defer c.cmu.Unlock()
	names := make([]string, 0, len(c.running))
	for n := range c.running {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st.MCP = append(st.MCP, cli.MCPServerStatusRPC{Name: n, Connected: c.running[n]})
	}
	return st
}

func (c *controlsBackend) SetMCPServer(name string, on bool) error {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	if _, ok := c.running[name]; !ok {
		return errors.New("unknown server " + name)
	}
	c.running[name] = on
	return nil
}

func (c *controlsBackend) SkillState() (pinned, manualOnly []string) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	for n := range c.pinned {
		pinned = append(pinned, n)
	}
	sort.Strings(pinned)
	return pinned, c.manual
}

func (c *controlsBackend) SetSkillPinned(name string, on bool) error {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	if name == "deploy" && on {
		return errors.New("manual only")
	}
	if on {
		c.pinned[name] = true
	} else {
		delete(c.pinned, name)
	}
	return nil
}

func (c *controlsBackend) Complete(line string) []Completion {
	c.cmu.Lock()
	c.lastLine = line
	c.cmu.Unlock()
	return []Completion{{Text: "start", Description: "start a server"}, {Text: "stop"}}
}

func startControls(t *testing.T) (*Server, *controlsBackend) {
	t.Helper()
	cb := newControlsBackend()
	srv, err := Start(Options{Backend: cb, PermissionTimeout: 2 * time.Second, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv, cb
}

func decode(t *testing.T, body []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("bad JSON %q: %v", body, err)
	}
}

func TestControls_MCPSwitchStartsAndStopsServers(t *testing.T) {
	srv, cb := startControls(t)

	code, body := call(t, srv, http.MethodPost, "/api/mcp/git", map[string]bool{"on": true}, true)
	if code != http.StatusOK {
		t.Fatalf("start: %d %s", code, body)
	}
	var got struct {
		MCP []cli.MCPServerStatusRPC `json:"mcp"`
	}
	decode(t, body, &got)
	if len(got.MCP) != 2 || got.MCP[1].Name != "git" || !got.MCP[1].Connected {
		t.Fatalf("the reply must carry the new MCP state, got %+v", got.MCP)
	}

	if code, body := call(t, srv, http.MethodPost, "/api/mcp/fs", map[string]bool{"on": false}, true); code != http.StatusOK {
		t.Fatalf("stop: %d %s", code, body)
	}
	cb.cmu.Lock()
	fs := cb.running["fs"]
	cb.cmu.Unlock()
	if fs {
		t.Fatal("fs should be stopped")
	}

	// An engine error comes back as the message the page shows.
	code, body = call(t, srv, http.MethodPost, "/api/mcp/nope", map[string]bool{"on": true}, true)
	if code != http.StatusConflict || !json.Valid(body) {
		t.Fatalf("unknown server: %d %s", code, body)
	}
	// "on" is required: an empty body must not stop a server by default.
	if code, _ := call(t, srv, http.MethodPost, "/api/mcp/git", map[string]string{}, true); code != http.StatusBadRequest {
		t.Fatalf("missing on: got %d, want 400", code)
	}
	// The switches are API calls like the others: no token, no change.
	if code, _ := call(t, srv, http.MethodPost, "/api/mcp/git", map[string]bool{"on": false}, false); code == http.StatusOK {
		t.Fatal("an unauthenticated call must not switch a server")
	}
}

func TestControls_SkillSwitchPinsAndReportsState(t *testing.T) {
	srv, _ := startControls(t)

	code, body := call(t, srv, http.MethodPost, "/api/skills/zeta", map[string]bool{"pinned": true}, true)
	if code != http.StatusOK {
		t.Fatalf("pin: %d %s", code, body)
	}
	var got struct {
		Pinned     []string `json:"pinned"`
		ManualOnly []string `json:"manual_only"`
		Controls   bool     `json:"controls"`
		Skills     []struct{ Name string }
	}
	decode(t, body, &got)
	if len(got.Pinned) != 1 || got.Pinned[0] != "zeta" || !got.Controls || len(got.Skills) != 1 || len(got.ManualOnly) != 1 {
		t.Fatalf("pin reply: %+v", got)
	}

	if code, body := call(t, srv, http.MethodPost, "/api/skills/deploy", map[string]bool{"pinned": true}, true); code != http.StatusConflict {
		t.Fatalf("manual-only skill must be refused: %d %s", code, body)
	}

	code, body = call(t, srv, http.MethodPost, "/api/skills/zeta", map[string]bool{"pinned": false}, true)
	if code != http.StatusOK {
		t.Fatalf("unpin: %d %s", code, body)
	}
	got.Pinned = nil
	decode(t, body, &got)
	if len(got.Pinned) != 0 {
		t.Fatalf("unpin left %v pinned", got.Pinned)
	}

	// The skill list and the boot carry the same state.
	_, body = call(t, srv, http.MethodGet, "/api/skills", nil, true)
	decode(t, body, &got)
	if !got.Controls || got.Pinned == nil {
		t.Fatalf("GET skills must carry the switch state: %s", body)
	}
	_, body = call(t, srv, http.MethodGet, "/api/boot", nil, true)
	var boot map[string]json.RawMessage
	decode(t, body, &boot)
	for _, k := range []string{"skills", "pinned", "manual_only", "controls"} {
		if _, ok := boot[k]; !ok {
			t.Errorf("boot lacks %q", k)
		}
	}
}

func TestControls_CompleteForwardsSlashLinesOnly(t *testing.T) {
	srv, cb := startControls(t)

	code, body := call(t, srv, http.MethodGet, "/api/complete?line="+url.QueryEscape("/mcp st"), nil, true)
	if code != http.StatusOK {
		t.Fatalf("complete: %d %s", code, body)
	}
	var got struct{ Items []Completion }
	decode(t, body, &got)
	if len(got.Items) != 2 || got.Items[0].Text != "start" || got.Items[0].Description == "" {
		t.Fatalf("completions: %+v", got.Items)
	}
	cb.cmu.Lock()
	line := cb.lastLine
	cb.cmu.Unlock()
	if line != "/mcp st" {
		t.Fatalf("the completer got %q, want the line as typed", line)
	}

	// A message is not a command line: no completer call, an empty list.
	cb.cmu.Lock()
	cb.lastLine = ""
	cb.cmu.Unlock()
	_, body = call(t, srv, http.MethodGet, "/api/complete?line="+url.QueryEscape("hello there"), nil, true)
	decode(t, body, &got)
	cb.cmu.Lock()
	line = cb.lastLine
	cb.cmu.Unlock()
	if len(got.Items) != 0 || line != "" {
		t.Fatalf("plain text must not complete: %+v (completer saw %q)", got.Items, line)
	}
}

func TestControls_BackendWithoutControlsStaysReadOnly(t *testing.T) {
	srv, _ := startTest(t)

	if code, _ := call(t, srv, http.MethodPost, "/api/mcp/fs", map[string]bool{"on": true}, true); code != http.StatusNotImplemented {
		t.Fatalf("MCP switch without controls: got %d, want 501", code)
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/skills/zeta", map[string]bool{"pinned": true}, true); code != http.StatusNotImplemented {
		t.Fatalf("skill switch without controls: got %d, want 501", code)
	}
	_, body := call(t, srv, http.MethodGet, "/api/complete?line="+url.QueryEscape("/mcp "), nil, true)
	var got struct{ Items []Completion }
	decode(t, body, &got)
	if got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("complete without controls must be an empty list, got %s", body)
	}
	_, body = call(t, srv, http.MethodGet, "/api/boot", nil, true)
	var boot map[string]json.RawMessage
	decode(t, body, &boot)
	if _, ok := boot["controls"]; ok {
		t.Fatal("boot must not advertise controls the backend lacks")
	}
	if _, ok := boot["skills"]; !ok {
		t.Fatal("boot lost the skill list")
	}
}
