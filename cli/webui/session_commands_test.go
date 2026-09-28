/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/i18n"
)

// editBackend adds the page's session capabilities to the scripted engine:
// a turn list the rewind and retry act on, a plan arm, a template, a skill
// and the web command runner.
type editBackend struct {
	*fakeBackend
	emu       sync.Mutex
	turns     []TurnRecord
	rewinds   []int
	restored  bool
	armed     bool
	webLines  []string
	failChat  bool
	chatTexts []string
}

func newEditBackend() *editBackend { return &editBackend{fakeBackend: newFakeBackend()} }

func (e *editBackend) Commands() []rpcserve.CommandInfo {
	return []rpcserve.CommandInfo{{Name: "memory"}, {Name: "session"}, {Name: "jobs"}, {Name: "review"}}
}

func (e *editBackend) LastTurn(string) (TurnRecord, bool) {
	e.emu.Lock()
	defer e.emu.Unlock()
	if len(e.turns) == 0 {
		return TurnRecord{}, false
	}
	return e.turns[len(e.turns)-1], true
}

func (e *editBackend) RewindTurns(session string, n int) (RewindResult, error) {
	e.emu.Lock()
	defer e.emu.Unlock()
	e.rewinds = append(e.rewinds, n)
	if n > len(e.turns) {
		n = len(e.turns)
	}
	removed := append([]TurnRecord(nil), e.turns[len(e.turns)-n:]...)
	e.turns = e.turns[:len(e.turns)-n]
	e.mu.Lock()
	e.sessions[session] = []rpcserve.HistoryItem{{Role: "user", Content: "kept"}}
	e.mu.Unlock()
	return RewindResult{Turns: n, Messages: 2 * n, Restore: func() bool {
		e.emu.Lock()
		defer e.emu.Unlock()
		e.turns = append(e.turns, removed...)
		e.restored = true
		return true
	}}, nil
}

func (e *editBackend) PlanCommand(_ context.Context, line string) (string, string, string) {
	e.emu.Lock()
	e.armed = true
	e.emu.Unlock()
	rest := strings.TrimSpace(strings.TrimPrefix(line, "/plan"))
	if strings.HasPrefix(rest, "coder ") {
		return "coder", strings.TrimPrefix(rest, "coder "), ""
	}
	if rest == "" {
		return "", "", "plan-first armed"
	}
	return "agent", rest, ""
}

func (e *editBackend) ExpandSlashCommand(_ context.Context, _, text string) (string, bool) {
	if strings.HasPrefix(text, "/review") {
		return "Review this: " + strings.TrimSpace(strings.TrimPrefix(text, "/review")), true
	}
	return "", false
}

func (e *editBackend) StageSkill(name, args string) (string, string, bool) {
	switch name {
	case "zeta":
		return "skill prompt " + args, "", true
	case "hidden":
		return "", "not invocable: /hidden", true
	}
	return "", "", false
}

func (e *editBackend) RunWebCommand(_ context.Context, _, line string) (string, error) {
	e.emu.Lock()
	e.webLines = append(e.webLines, line)
	e.emu.Unlock()
	return "web ran " + line, nil
}

func (e *editBackend) Chat(ctx context.Context, session, text string, o TurnOptions, sink cli.ChunkSink) (string, error) {
	e.emu.Lock()
	e.chatTexts = append(e.chatTexts, text)
	fail := e.failChat
	e.emu.Unlock()
	if fail {
		return "", errors.New("model down")
	}
	return e.fakeBackend.Chat(ctx, session, text, o, sink)
}

func startEdit(t *testing.T) (*Server, *editBackend) {
	t.Helper()
	i18n.Init()
	eb := newEditBackend()
	srv, err := Start(Options{Backend: eb, PermissionTimeout: 2 * time.Second, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv, eb
}

func findEvent(evs []event, typ string) *event {
	for i := range evs {
		if evs[i].Type == typ {
			return &evs[i]
		}
	}
	return nil
}

// A slash-command template runs as a turn with its expanded prompt. It used
// to run as a headless command, whose handler fires the template's turn in
// the background and prints nothing — the page then showed "(command
// produced no output)" for every template.
func TestWebCommands_TemplateRunsAsTurn(t *testing.T) {
	srv, eb := startEdit(t)
	evs := turnEvents(t, srv, "chat", "/review 12")
	done := evs[len(evs)-1]
	if done.Type != "done" || done.Reply != "hello Review this: 12" {
		t.Fatalf("template: %s %+v", types(evs), evs)
	}
	if len(eb.webLines) != 0 {
		t.Fatalf("a template must not run as a headless command: %v", eb.webLines)
	}
}

// /clear is the page's "+ New": a fresh bound session, the previous one
// named as kept when it is saved, and /newsession and /session new take the
// same path instead of leaving the page unbound.
func TestWebCommands_ClearStartsAFreshBoundSession(t *testing.T) {
	srv, eb := startEdit(t)
	eb.bound["web"] = "s1"
	prev := "s1"
	for _, line := range []string{"/clear", "/newsession", "/session new"} {
		evs := turnEvents(t, srv, "chat", line)
		se := findEvent(evs, "session")
		if se == nil || se.Session == nil || !strings.HasPrefix(se.Session.Bound, SessionPrefix) || len(se.Session.History) != 0 {
			t.Fatalf("%s: %s %+v", line, types(evs), evs)
		}
		// Three new conversations within one second are three sessions.
		if se.Session.Bound == prev {
			t.Fatalf("%s rebound the conversation just left: %q", line, prev)
		}
		prev = se.Session.Bound
		if line == "/clear" && !strings.Contains(se.Session.Notice, "s1") {
			t.Fatalf("the notice must name the saved previous session: %q", se.Session.Notice)
		}
		if evs[len(evs)-1].Type != "done" {
			t.Fatalf("%s must end with done: %s", line, types(evs))
		}
	}
	if len(eb.seen) != 0 {
		t.Fatalf("/clear reached the model: %+v", eb.seen)
	}
}

// Other /session subcommands run and then re-sync the page with the
// session they left it on.
func TestWebCommands_SessionCommandResyncsThePage(t *testing.T) {
	srv, eb := startEdit(t)
	evs := turnEvents(t, srv, "chat", "/session load s1")
	if types(evs) != "run,session,done" || evs[2].Reply != "web ran /session load s1" {
		t.Fatalf("session: %s %+v", types(evs), evs)
	}
	if len(eb.webLines) != 1 {
		t.Fatalf("the command must run once: %v", eb.webLines)
	}
}

func TestWebCommands_Rewind(t *testing.T) {
	srv, eb := startEdit(t)
	eb.turns = []TurnRecord{{Mode: "chat", Text: "a"}, {Mode: "chat", Text: "b"}, {Mode: "chat", Text: "c"}}

	evs := turnEvents(t, srv, "chat", "/rewind 2")
	se := findEvent(evs, "session")
	if se == nil || len(se.Session.History) != 1 || !strings.Contains(se.Session.Notice, "2") || evs[len(evs)-1].Type != "done" {
		t.Fatalf("rewind 2: %s %+v", types(evs), evs)
	}
	if len(eb.turns) != 1 || eb.rewinds[0] != 2 {
		t.Fatalf("rewind must drop 2 turns: %+v %v", eb.turns, eb.rewinds)
	}
	for _, bad := range []string{"/rewind x", "/rewind 0", "/rewind compact"} {
		evs = turnEvents(t, srv, "chat", bad)
		if types(evs) != "run,done" || evs[1].Reply == "" || len(eb.rewinds) != 1 {
			t.Fatalf("%s must answer the usage without rewinding: %s %+v", bad, types(evs), evs)
		}
	}
	eb.turns = nil
	evs = turnEvents(t, srv, "chat", "/rewind")
	if types(evs) != "run,done" || evs[1].Reply == "" {
		t.Fatalf("nothing to rewind: %s %+v", types(evs), evs)
	}
}

// /retry asks the last turn again as it was asked — its mode, not the
// page's — in place of its reply; a retry that fails puts the turn back.
func TestWebCommands_Retry(t *testing.T) {
	srv, eb := startEdit(t)
	evs := turnEvents(t, srv, "chat", "/retry")
	if types(evs) != "run,done" || evs[1].Reply == "" {
		t.Fatalf("nothing to retry: %s %+v", types(evs), evs)
	}

	eb.turns = []TurnRecord{{Mode: "chat", Text: "first"}, {Mode: "coder", Text: "fix it"}}
	evs = turnEvents(t, srv, "chat", "/retry")
	se := findEvent(evs, "session")
	if se == nil || len(se.Session.History) != 2 || se.Session.History[1]["content"] != "fix it" {
		t.Fatalf("the page must show the retried turn: %s %+v", types(evs), evs)
	}
	if findEvent(evs, "permission") == nil {
		t.Fatalf("a coder turn must be retried in coder mode: %s", types(evs))
	}
	if eb.rewinds[0] != 1 {
		t.Fatalf("retry must rewind exactly one turn: %v", eb.rewinds)
	}

	eb.turns = []TurnRecord{{Mode: "chat", Text: "again"}}
	eb.failChat = true
	evs = turnEvents(t, srv, "coder", "/retry")
	if !eb.restored || evs[len(evs)-1].Type != "error" {
		t.Fatalf("a failed retry must restore the turn: restored=%v %s", eb.restored, types(evs))
	}
	var sessions int
	for _, ev := range evs {
		if ev.Type == "session" {
			sessions++
		}
	}
	if sessions != 2 || eb.chatTexts[len(eb.chatTexts)-1] != "again" {
		t.Fatalf("the restore must re-sync the page: %s %v", types(evs), eb.chatTexts)
	}
}

// /plan with a task runs it plan-first in the mode /plan names; bare /plan
// arms Plan-First and shows the session's last plan.
func TestWebCommands_Plan(t *testing.T) {
	srv, eb := startEdit(t)
	evs := turnEvents(t, srv, "chat", "/plan")
	if types(evs) != "run,done" || !strings.Contains(evs[1].Reply, "plan-first armed") || !eb.armed {
		t.Fatalf("bare plan: %s %+v", types(evs), evs)
	}
	srv.rememberPlan("web", []planEntry{{Content: "step one", Status: "in_progress"}})
	evs = turnEvents(t, srv, "chat", "/plan")
	if p := findEvent(evs, "plan"); p == nil || len(p.Plan) != 1 || p.Plan[0].Content != "step one" {
		t.Fatalf("bare plan must show the last plan: %s %+v", types(evs), evs)
	}
	evs = turnEvents(t, srv, "chat", "/plan coder add tests")
	if findEvent(evs, "permission") == nil || !strings.Contains(evs[len(evs)-1].Reply, "done: add tests") {
		t.Fatalf("plan coder: %s %+v", types(evs), evs)
	}
}

func TestWebCommands_SkillsAndRefusals(t *testing.T) {
	srv, eb := startEdit(t)
	evs := turnEvents(t, srv, "chat", "/zeta the diff")
	if evs[len(evs)-1].Reply != "hello skill prompt the diff" {
		t.Fatalf("skill: %s %+v", types(evs), evs)
	}
	evs = turnEvents(t, srv, "chat", "/hidden")
	if types(evs) != "run,done" || evs[1].Reply != "not invocable: /hidden" {
		t.Fatalf("refused skill: %s %+v", types(evs), evs)
	}
	for _, line := range []string{"/exit", "/quit", "/wait --until x", "/reload", "/menu", "/auth login openai", "/retryall"} {
		evs = turnEvents(t, srv, "chat", line)
		token, _ := splitLeadingToken(line)
		if types(evs) != "run,done" || !strings.Contains(evs[1].Reply, token) || strings.Contains(evs[1].Reply, "web.command") {
			t.Fatalf("%s: %s %+v", line, types(evs), evs)
		}
	}
	// Advertised commands run under the page's rules.
	evs = turnEvents(t, srv, "chat", "/jobs list")
	if evs[len(evs)-1].Reply != "web ran /jobs list" {
		t.Fatalf("jobs: %s %+v", types(evs), evs)
	}
	if len(eb.seen) != 1 {
		t.Fatalf("only the skill turn may reach the model: %+v", eb.seen)
	}
}

// The command endpoint runs only what the page advertises.
func TestWebCommands_CommandEndpointRunsOnlyAdvertised(t *testing.T) {
	srv, eb := startEdit(t)
	if code, body := call(t, srv, http.MethodPost, "/api/command", map[string]string{"line": "/session fork x"}, true); code != http.StatusOK || !strings.Contains(string(body), "web ran /session fork x") {
		t.Fatalf("advertised = %d %s", code, body)
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/command", map[string]string{"line": "/web stop"}, true); code != http.StatusBadRequest {
		t.Fatalf("unadvertised command = %d, want 400", code)
	}
	if len(eb.webLines) != 1 {
		t.Fatalf("only the advertised command may run: %v", eb.webLines)
	}
}

// The page applies the server's session events (a new conversation, a
// rewind, a retry) the way "+ New" and attach do: bound name, transcript,
// panels, and the notice as a toast.
func TestPage_AppliesSessionEvents(t *testing.T) {
	i18n.Init()
	page := string(Page())
	for _, want := range []string{
		"case 'session': applySession(ev.session || {}); break;",
		"function applySession(se)",
		"state.bound = se.bound || ''; loadHistory(se.history); renderSessions(); renderStatus(); refreshSessions();",
		"if (se.notice) toast(se.notice, 'ok');",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// Every string the session commands can show resolves in every page
	// language, so no raw key ever reaches the transcript.
	for _, lang := range UILanguages {
		for _, key := range []string{"web.command.clear.kept", "web.command.rewind.done", "web.command.retry.running", "web.command.plan.how", "web.command.refuse.exit", "web.command.refuse.wait"} {
			if v, ok := i18n.LookupIn(lang, key); !ok || v == "" {
				t.Errorf("%s: %s unresolved", lang, key)
			}
		}
	}
}
