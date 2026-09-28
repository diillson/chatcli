/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/webui"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

// e2eReply is what the shared recording model answers a chat turn.
const e2eReply = "I looked at it."

// asksOf counts the requests whose last typed user message contains text:
// the turns that asked it, not the later ones that carry it as history.
func asksOf(m *e2eModel, text string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if last := lastUserMessage(c); last != nil && strings.Contains(last.Content, text) {
			n++
		}
	}
	return n
}

// startSessCmdWeb is the shared web harness with the browser's session
// bound to a saved one, as `chatcli web` binds it, and a template
// installed in the isolated home.
func startSessCmdWeb(t *testing.T) *e2eWeb {
	t.Helper()
	i18n.Init()
	t.Setenv("CHATCLI_SCHEDULER_ENABLED", "false")
	w := startE2EWeb(t)
	cmds := filepath.Join(w.home, ".chatcli", "commands")
	if err := os.MkdirAll(cmds, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmds, "hello.md"), []byte("---\ndescription: greet\n---\nSay hello to $ARGUMENTS\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.engine.SaveSessionRPC("web-e2e", nil); err != nil {
		t.Fatal(err)
	}
	if code, body := sessCmdCall(t, w, "/api/session", map[string]string{"action": "attach", "session": webLiveSession, "name": "web-e2e"}); code != http.StatusOK {
		t.Fatalf("attach = %d %s", code, body)
	}
	return w
}

func sessCmdCall(t *testing.T, w *e2eWeb, path string, body interface{}) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+w.srv.Host()+path, bytes.NewReader(b))
	req.Header.Set("X-Web-Token", w.srv.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp.StatusCode, out.Bytes()
}

type sessCmdEvent struct {
	Type    string `json:"type"`
	Reply   string `json:"reply"`
	Error   string `json:"error"`
	Session *struct {
		Bound   string              `json:"bound"`
		History []map[string]string `json:"history"`
		Notice  string              `json:"notice"`
	} `json:"session"`
}

// events posts one page turn and returns every event of it.
func (w *e2eWeb) events(t *testing.T, mode, text string) []sessCmdEvent {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"session": webLiveSession, "mode": mode, "text": text})
	req, _ := http.NewRequest(http.MethodPost, "http://"+w.srv.Host()+"/api/turn", bytes.NewReader(body))
	req.Header.Set("X-Web-Token", w.srv.Token())
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("turn %q: status %d", text, resp.StatusCode)
	}
	var out []sessCmdEvent
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			var ev sessCmdEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("frame %q: %v", line, err)
			}
			out = append(out, ev)
		}
	}
	return out
}

func (w *e2eWeb) last(t *testing.T, mode, text string) sessCmdEvent {
	t.Helper()
	evs := w.events(t, mode, text)
	if len(evs) == 0 {
		t.Fatalf("turn %q: no events", text)
	}
	return evs[len(evs)-1]
}

func (w *e2eWeb) sessionEvent(t *testing.T, evs []sessCmdEvent) sessCmdEvent {
	t.Helper()
	for _, ev := range evs {
		if ev.Type == "session" && ev.Session != nil {
			return ev
		}
	}
	t.Fatalf("no session event in %+v", evs)
	return sessCmdEvent{}
}

// conversation is what a person would read of a history: the typed user
// messages and the replies.
func conversation(hist []models.Message) []string {
	var out []string
	for _, m := range hist {
		if (m.Role == "user" && !m.IsInjectedContext()) || m.Role == "assistant" {
			out = append(out, m.Role+":"+m.Content)
		}
	}
	return out
}

func (w *e2eWeb) saved(t *testing.T) []string {
	t.Helper()
	hist, err := w.engine.LoadSessionRPC("web-e2e")
	if err != nil {
		t.Fatalf("saved session: %v", err)
	}
	return conversation(hist)
}

func TestWebSessionCommands_E2E(t *testing.T) {
	w := startSessCmdWeb(t)

	first := w.last(t, "chat", "first question")
	second := w.last(t, "chat", "second question")
	if first.Type != "done" || second.Type != "done" || first.Reply != e2eReply || second.Reply != e2eReply {
		t.Fatalf("turns: %+v %+v", first, second)
	}
	want := []string{"user:first question", "assistant:" + e2eReply, "user:second question", "assistant:" + e2eReply}
	if got := w.saved(t); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("saved before retry = %v", got)
	}

	// /retry asks the second question again: the page is re-synced to the
	// conversation without that turn plus the question, the model is
	// asked again and the new reply replaces the old one, live and saved.
	evs := w.events(t, "chat", "/retry")
	se := w.sessionEvent(t, evs)
	if len(se.Session.History) != 3 || se.Session.History[2]["content"] != "second question" || se.Session.Bound != "web-e2e" {
		t.Fatalf("retry session event = %+v", se.Session)
	}
	if retried := evs[len(evs)-1]; retried.Type != "done" || retried.Reply != e2eReply {
		t.Fatalf("retry final = %+v", retried)
	}
	if n := asksOf(w.model, "second question"); n != 2 {
		t.Fatalf("the model must be asked the second question twice, got %d", n)
	}
	if got := w.saved(t); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("saved after retry = %v", got)
	}

	// /rewind drops the last turn, live and saved; /rewind past the start
	// leaves the conversation empty, saved as empty too.
	evs = w.events(t, "chat", "/rewind")
	se = w.sessionEvent(t, evs)
	if len(se.Session.History) != 2 || !strings.Contains(se.Session.Notice, "1 turn") {
		t.Fatalf("rewind session event = %+v", se.Session)
	}
	if got := w.saved(t); strings.Join(got, "|") != strings.Join(want[:2], "|") {
		t.Fatalf("saved after rewind = %v", got)
	}
	evs = w.events(t, "chat", "/rewind 5")
	if se = w.sessionEvent(t, evs); len(se.Session.History) != 0 {
		t.Fatalf("rewind all = %+v", se.Session)
	}
	if got := w.saved(t); len(got) != 0 {
		t.Fatalf("saved after rewinding everything = %v", got)
	}
	if ev := w.last(t, "chat", "/rewind"); ev.Type != "done" || ev.Reply == "" || strings.Contains(ev.Reply, "web.command") {
		t.Fatalf("nothing to rewind: %+v", ev)
	}

	// A template runs as the turn it stands for, not as a command that
	// printed nothing.
	if greeted := w.last(t, "chat", "/hello world"); greeted.Type != "done" || greeted.Reply != e2eReply {
		t.Fatalf("template: %+v", greeted)
	}
	if n := asksOf(w.model, "Say hello to world"); n != 1 {
		t.Fatalf("the template's prompt must reach the model once, got %d", n)
	}

	// Headless commands the page adds answer with their output.
	for _, line := range []string{"/hooks", "/plan", "/jobs", "/schedule list"} {
		ev := w.last(t, "coder", line)
		if ev.Type != "done" || ev.Reply == "" || strings.Contains(ev.Reply, "produced no output") || strings.Contains(ev.Reply, "web.command") {
			t.Fatalf("%s: %+v", line, ev)
		}
	}

	// /clear binds a fresh web session and keeps the previous one saved.
	evs = w.events(t, "chat", "/clear")
	se = w.sessionEvent(t, evs)
	if !strings.HasPrefix(se.Session.Bound, webui.SessionPrefix) || se.Session.Bound == "web-e2e" || len(se.Session.History) != 0 || !strings.Contains(se.Session.Notice, "web-e2e") {
		t.Fatalf("clear = %+v", se.Session)
	}
	if got := w.saved(t); strings.Join(got, "|") != "user:Say hello to world|assistant:"+e2eReply {
		t.Fatalf("the previous session must stay saved: %v", got)
	}
}
