/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2026 Edilson Freitas
 * License: MIT
 */

package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func turnEvents(t *testing.T, srv *Server, mode, text string) []event {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"mode": mode, "text": text})
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Host()+"/api/turn", bytes.NewReader(b))
	req.Header.Set(tokenHeader, srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("turn = %d", resp.StatusCode)
	}
	return readSSE(t, resp)
}

func types(evs []event) string {
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return strings.Join(out, ",")
}

func TestRouteInline_SlashCommandsAndModes(t *testing.T) {
	srv, fb := startTest(t)

	// Advertised command runs headless; its output is the reply.
	evs := turnEvents(t, srv, "chat", "/memory list")
	if types(evs) != "run,done" || evs[1].Reply != "ran /memory list" {
		t.Fatalf("command: %s %+v", types(evs), evs)
	}
	if len(fb.seen) != 0 {
		t.Fatalf("command reached the model: %+v", fb.seen)
	}

	// Bare mode switch: mode event, no model call.
	evs = turnEvents(t, srv, "chat", "/coder")
	if types(evs) != "run,mode,done" || evs[1].Mode != "coder" || evs[1].Text == "" {
		t.Fatalf("mode: %s %+v", types(evs), evs)
	}

	// Mode switch with a task: switches, then the task runs in the new mode.
	evs = turnEvents(t, srv, "coder", "/chat say hi")
	if types(evs) != "run,mode,chunk,chunk,done" || evs[1].Mode != "chat" || evs[4].Reply != "hello say hi" {
		t.Fatalf("mode+task: %s %+v", types(evs), evs)
	}

	// Known REPL command that is not headless-capable answers clearly.
	evs = turnEvents(t, srv, "chat", "/exit")
	if types(evs) != "run,done" || !strings.Contains(evs[1].Reply, "/exit") {
		t.Fatalf("unsupported: %s %+v", types(evs), evs)
	}

	// Unknown slash token is plain user text.
	evs = turnEvents(t, srv, "chat", "/usr/local/bin is on PATH")
	if types(evs) != "run,chunk,chunk,done" {
		t.Fatalf("path: %s", types(evs))
	}
}

func TestRouteInline_LeadingToolRunsDirectlyInChat(t *testing.T) {
	srv, fb := startTest(t)

	evs := turnEvents(t, srv, "chat", "@read --path go.mod")
	if types(evs) != "run,tool_start,tool_end,done" {
		t.Fatalf("tool: %s", types(evs))
	}
	if evs[1].Tool.Name != "@read" || evs[1].Tool.Input != "--path go.mod" || evs[2].Tool.Output != "tool-out" || evs[2].Tool.IsError {
		t.Fatalf("tool events: %+v %+v", evs[1].Tool, evs[2].Tool)
	}
	if len(fb.seen) != 0 {
		t.Fatalf("tool line reached the model: %+v", fb.seen)
	}

	// Chat context mentions and unknown tools stay user text for the turn.
	for _, line := range []string{"@git status", "@nosuchtool x", "@file go.mod explain"} {
		evs = turnEvents(t, srv, "chat", line)
		if types(evs) != "run,chunk,chunk,done" {
			t.Fatalf("%q: %s", line, types(evs))
		}
	}

	// In coder mode the agent owns tool calls: the line is a task.
	evs = turnEvents(t, srv, "coder", "@read go.mod")
	if len(evs) < 2 || evs[1].Type != "thought" {
		t.Fatalf("coder mode did not hand the line to the agent: %s", types(evs))
	}
}
