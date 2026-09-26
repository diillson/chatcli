/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/agentevents"
	"github.com/diillson/chatcli/cli/rpcserve"
	"github.com/diillson/chatcli/models"
)

// fakeBackend scripts the engine: chat streams its reply in two chunks,
// coder asks for permission before answering, agent blocks until cancelled.
type fakeBackend struct {
	mu       sync.Mutex
	provider string
	model    string
	seen     []TurnOptions
	sessions map[string][]rpcserve.HistoryItem
	bound    map[string]string
	decision agentevents.PermissionDecision
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{provider: "FAKE", model: "fake-1", sessions: map[string][]rpcserve.HistoryItem{}, bound: map[string]string{}}
}

func (f *fakeBackend) HasLLM() bool                   { return true }
func (f *fakeBackend) Defaults() (string, string)     { return f.provider, f.model }
func (f *fakeBackend) SetDefaults(p, m string)        { f.provider, f.model = p, m }
func (f *fakeBackend) ProvidersJSON() (string, error) { return `{"active_provider":"FAKE"}`, nil }
func (f *fakeBackend) Tools() []rpcserve.ToolInfo     { return []rpcserve.ToolInfo{{Name: "@read"}} }
func (f *fakeBackend) CallTool(context.Context, string, string) (string, error) {
	return "tool-out", nil
}
func (f *fakeBackend) Skills() []rpcserve.SkillInfo        { return []rpcserve.SkillInfo{{Name: "zeta"}} }
func (f *fakeBackend) SkillContent(string) (string, error) { return "ZETA", nil }
func (f *fakeBackend) Commands() []rpcserve.CommandInfo {
	return []rpcserve.CommandInfo{{Name: "/memory"}}
}
func (f *fakeBackend) RunCommand(_ context.Context, _, line string) (string, error) {
	return "ran " + line, nil
}
func (f *fakeBackend) ManageSession(_ context.Context, action, session, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch action {
	case "attach":
		f.bound[session] = name
		if !strings.HasPrefix(name, SessionPrefix) {
			f.sessions[session] = []rpcserve.HistoryItem{{Role: "user", Content: "from " + name}}
		}
		return "attached", nil
	case "clear":
		delete(f.sessions, session)
		delete(f.bound, session)
		return "cleared", nil
	}
	return "", errors.New("unsupported")
}
func (f *fakeBackend) BoundSession(session string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bound[session]
}
func (f *fakeBackend) RestoreSession(_ context.Context, session string) ([]rpcserve.HistoryItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[session], nil
}
func (f *fakeBackend) SessionCatalog() []cli.SessionSummaryRPC {
	return []cli.SessionSummaryRPC{{Name: "s1", Title: "first"}}
}
func (f *fakeBackend) SessionMessages(string, int, int) ([]models.Message, int, error) {
	return []models.Message{{Role: "user", Content: "hi"}}, 1, nil
}
func (f *fakeBackend) Resources() []rpcserve.ResourceInfo { return nil }
func (f *fakeBackend) ReadResource(context.Context, string) (rpcserve.ResourceContent, error) {
	return rpcserve.ResourceContent{}, errors.New("none")
}
func (f *fakeBackend) Status() Status {
	return Status{Version: "t", Provider: f.provider, Model: f.model}
}

func (f *fakeBackend) Chat(_ context.Context, _, text string, o TurnOptions, sink cli.ChunkSink) (string, error) {
	f.mu.Lock()
	f.seen = append(f.seen, o)
	f.mu.Unlock()
	sink.Chunk("hel")
	sink.Chunk("lo " + text)
	return "hello " + text, nil
}

func (f *fakeBackend) RunCoder(ctx context.Context, _, task string, _ TurnOptions, events agentevents.Sink) (string, error) {
	events.Thought("thinking")
	events.ToolStart(agentevents.ToolCall{ID: "t1", Name: "@coder", Title: "rm -rf build"})
	d, err := events.(agentevents.PermissionDecider).RequestPermissionDecision(agentevents.PermissionRequest{Tool: agentevents.ToolCall{ID: "t1", Name: "@coder"}, Reason: "dangerous", OfferAlways: true})
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	f.decision = d
	f.mu.Unlock()
	events.ToolEnd(agentevents.ToolCall{ID: "t1", Name: "@coder", Status: "done", Output: "ok"})
	events.Message("done: " + task)
	return "done: " + task, nil
}

func (f *fakeBackend) RunAgent(ctx context.Context, _, _ string, _ TurnOptions, _ agentevents.Sink) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func startTest(t *testing.T) (*Server, *fakeBackend) {
	t.Helper()
	fb := newFakeBackend()
	srv, err := Start(Options{Backend: fb, PermissionTimeout: 2 * time.Second, Version: "t"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return srv, fb
}

func call(t *testing.T, srv *Server, method, path string, body interface{}, auth bool) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+srv.Host()+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if auth {
		req.Header.Set(tokenHeader, srv.Token())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// readSSE parses "data:" frames until the stream ends.
func readSSE(t *testing.T, resp *http.Response) []event {
	t.Helper()
	defer resp.Body.Close()
	var out []event
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func TestServer_RefusesNonLoopbackAndUnauthenticated(t *testing.T) {
	if _, err := Start(Options{Backend: newFakeBackend(), Addr: "0.0.0.0:0"}); err == nil {
		t.Fatal("a non-loopback address must be refused")
	}
	srv, _ := startTest(t)
	if code, _ := call(t, srv, http.MethodGet, "/api/status", nil, false); code != http.StatusForbidden {
		t.Fatalf("no token = %d, want 403", code)
	}
	if code, body := call(t, srv, http.MethodGet, "/api/status", nil, true); code != http.StatusOK || !strings.Contains(string(body), `"provider":"FAKE"`) {
		t.Fatalf("status = %d %s", code, body)
	}
	// The page carries the boot JSON with the token; the API token is never
	// accepted from the query.
	code, body := call(t, srv, http.MethodGet, "/?t="+srv.Token(), nil, false)
	if code != http.StatusOK || !strings.Contains(string(body), srv.Token()) {
		t.Fatalf("index = %d", code)
	}
	if code, _ := call(t, srv, http.MethodGet, "/api/status?t="+srv.Token(), nil, false); code != http.StatusForbidden {
		t.Fatal("token in the query must not authenticate")
	}
}

func TestServer_BootAndCatalogs(t *testing.T) {
	srv, _ := startTest(t)
	code, body := call(t, srv, http.MethodGet, "/api/boot", nil, true)
	if code != http.StatusOK {
		t.Fatalf("boot = %d %s", code, body)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"boot", "status", "providers", "tools", "skills", "commands", "sessions", "history"} {
		if _, ok := got[k]; !ok {
			t.Errorf("boot lacks %q", k)
		}
	}
	if !strings.Contains(string(got["boot"]), `"llm":true`) || strings.Contains(string(got["boot"]), `"voice":true`) {
		t.Fatalf("features = %s", got["boot"])
	}
	code, body = call(t, srv, http.MethodPost, "/api/defaults", map[string]string{"provider": "OTHER", "model": "m2"}, true)
	if code != http.StatusOK || !strings.Contains(string(body), "OTHER") {
		t.Fatalf("defaults = %d %s", code, body)
	}
	if code, body := call(t, srv, http.MethodPost, "/api/command", map[string]string{"line": "/memory"}, true); code != http.StatusOK || !strings.Contains(string(body), "ran /memory") {
		t.Fatalf("command = %d %s", code, body)
	}
	if code, body := call(t, srv, http.MethodPost, "/api/session", map[string]string{"action": "attach", "name": "s1"}, true); code != http.StatusOK || !strings.Contains(string(body), "from s1") || !strings.Contains(string(body), `"bound":"s1"`) {
		t.Fatalf("session attach must return the bound history and name: %d %s", code, body)
	}
	// Boot names the bound session so the page shows what it shares with
	// the terminal from the first paint.
	if code, body := call(t, srv, http.MethodGet, "/api/boot", nil, true); code != http.StatusOK || !strings.Contains(string(body), `"bound":"s1"`) {
		t.Fatalf("boot must name the bound session: %d %s", code, body)
	}
	// "+ New" clears the live session and binds a fresh web-owned one, so
	// the new conversation is written through instead of living only in
	// the autosave mirror.
	code, body = call(t, srv, http.MethodPost, "/api/session", map[string]string{"action": "clear"}, true)
	var cleared struct {
		Bound   string                 `json:"bound"`
		History []rpcserve.HistoryItem `json:"history"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &cleared) != nil || !strings.HasPrefix(cleared.Bound, SessionPrefix) || len(cleared.History) != 0 {
		t.Fatalf("clear must bind a fresh %s session with an empty history: %d %s", SessionPrefix, code, body)
	}
}

func TestServer_ChatTurnStreamsChunksAndImages(t *testing.T) {
	srv, fb := startTest(t)
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n0000"))
	body := map[string]interface{}{"mode": "chat", "text": "world", "images": []map[string]string{{"name": "a.png", "media_type": "image/png", "data": png}}}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Host()+"/api/turn", bytes.NewReader(b))
	req.Header.Set(tokenHeader, srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("turn = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := readSSE(t, resp)
	var types []string
	var text strings.Builder
	for _, ev := range events {
		types = append(types, ev.Type)
		if ev.Type == "chunk" {
			text.WriteString(ev.Text)
		}
	}
	if strings.Join(types, ",") != "run,chunk,chunk,done" || text.String() != "hello world" || events[3].Reply != "hello world" {
		t.Fatalf("events = %v text=%q", types, text.String())
	}
	if len(fb.seen) != 1 || len(fb.seen[0].Images) != 1 || fb.seen[0].Images[0].MediaType != "image/png" {
		t.Fatalf("image not delivered to the turn: %+v", fb.seen)
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/turn", map[string]string{"text": "x", "images": ""}, true); code != http.StatusBadRequest {
		t.Fatalf("bad body = %d", code)
	}
}

func TestServer_CoderPermissionDialogAndBusy(t *testing.T) {
	srv, fb := startTest(t)
	b, _ := json.Marshal(map[string]string{"mode": "coder", "text": "clean"})
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Host()+"/api/turn", bytes.NewReader(b))
	req.Header.Set(tokenHeader, srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	var runID, dialogID string
	next := func() event {
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				var ev event
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev)
				return ev
			}
		}
		t.Fatal("stream ended early")
		return event{}
	}
	if ev := next(); ev.Type != "run" {
		t.Fatalf("first event %s", ev.Type)
	} else {
		runID = ev.Run
	}
	// A second turn while one runs is refused, not queued.
	if code2, _ := call(t, srv, http.MethodPost, "/api/turn", map[string]string{"text": "again"}, true); code2 != http.StatusConflict {
		t.Fatalf("concurrent turn = %d, want 409", code2)
	}
	for {
		ev := next()
		if ev.Type == "permission" {
			if ev.Tool == nil || ev.Tool.Name != "@coder" || !ev.OfferAll {
				t.Fatalf("permission event = %+v", ev)
			}
			dialogID = ev.ID
			break
		}
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/runs/"+runID+"/permission", map[string]string{"id": "nope", "decision": "allow_once"}, true); code != http.StatusNotFound {
		t.Fatalf("unknown dialog = %d", code)
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/runs/"+runID+"/permission", map[string]string{"id": dialogID, "decision": "maybe"}, true); code != http.StatusBadRequest {
		t.Fatalf("bad decision = %d", code)
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/runs/"+runID+"/permission", map[string]string{"id": dialogID, "decision": "allow_always"}, true); code != http.StatusOK {
		t.Fatalf("decision = %d", code)
	}
	var sawToolEnd, sawMessage bool
	for {
		ev := next()
		if ev.Type == "tool_end" {
			sawToolEnd = true
		}
		if ev.Type == "message" {
			sawMessage = true
		}
		if ev.Type == "done" {
			if ev.Reply != "done: clean" || !sawToolEnd || !sawMessage {
				t.Fatalf("done=%+v toolEnd=%v message=%v", ev, sawToolEnd, sawMessage)
			}
			break
		}
	}
	if fb.decision != agentevents.PermissionAllowAlways {
		t.Fatalf("engine received %q", fb.decision)
	}
}

func TestServer_CancelStopsAgentAndPermissionTimesOutToDeny(t *testing.T) {
	srv, _ := startTest(t)
	b, _ := json.Marshal(map[string]string{"mode": "agent", "text": "loop"})
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Host()+"/api/turn", bytes.NewReader(b))
	req.Header.Set(tokenHeader, srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// One scanner for the whole stream: a second scanner on the same body
	// would miss the bytes the first one already buffered.
	sc := bufio.NewScanner(resp.Body)
	frame := func() (event, bool) {
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				var ev event
				_ = json.Unmarshal([]byte(strings.TrimPrefix(sc.Text(), "data: ")), &ev)
				return ev, true
			}
		}
		return event{}, false
	}
	first, ok := frame()
	if !ok || first.Type != "run" {
		t.Fatalf("first frame = %+v", first)
	}
	if code, _ := call(t, srv, http.MethodPost, "/api/runs/"+first.Run+"/cancel", nil, true); code != http.StatusOK {
		t.Fatalf("cancel = %d", code)
	}
	var last event
	for {
		ev, ok := frame()
		if !ok {
			break
		}
		last = ev
	}
	if last.Type != "done" || !last.Cancelled {
		t.Fatalf("after cancel the stream must end with a cancelled done, got %+v", last)
	}
	runID := first.Run
	if code, _ := call(t, srv, http.MethodPost, "/api/runs/"+runID+"/cancel", nil, true); code != http.StatusNotFound {
		t.Fatalf("cancel after the run = %d, want 404", code)
	}

	// A dialog nobody answers denies once after the timeout instead of
	// stalling the engine.
	sink := newRunSink(context.Background(), "r", 30*time.Millisecond)
	d, err := sink.RequestPermissionDecision(agentevents.PermissionRequest{Tool: agentevents.ToolCall{ID: "x"}})
	if err != nil || d != agentevents.PermissionDenyOnce {
		t.Fatalf("timeout decision = %q err=%v", d, err)
	}
}

// Every read endpoint answers through the backend, media endpoints report
// they are off when no provider is configured, and unknown paths or
// actions are 404s rather than silent successes.
func TestServer_CatalogsMediaAndErrors(t *testing.T) {
	srv, _ := startTest(t)
	type probe struct {
		method, path string
		body         interface{}
		want         int
		contains     string
	}
	probes := []probe{
		{http.MethodGet, "/api/providers", nil, http.StatusOK, `"active_provider":"FAKE"`},
		{http.MethodGet, "/api/sessions", nil, http.StatusOK, `"first"`},
		{http.MethodGet, "/api/sessions/s1/messages?limit=5", nil, http.StatusOK, `"total":1`},
		{http.MethodGet, "/api/sessions/s1/nope", nil, http.StatusNotFound, ""},
		{http.MethodGet, "/api/history", nil, http.StatusOK, `"history"`},
		{http.MethodGet, "/api/tools", nil, http.StatusOK, `@read`},
		{http.MethodPost, "/api/tools/@read", map[string]string{"args": "x"}, http.StatusOK, "tool-out"},
		{http.MethodGet, "/api/skills", nil, http.StatusOK, "zeta"},
		{http.MethodGet, "/api/skills/zeta", nil, http.StatusOK, "ZETA"},
		{http.MethodGet, "/api/commands", nil, http.StatusOK, "/memory"},
		{http.MethodGet, "/api/resources", nil, http.StatusOK, `"resources"`},
		{http.MethodGet, "/api/resource?uri=chatcli://x", nil, http.StatusNotFound, "none"},
		{http.MethodPost, "/api/tts", map[string]string{"text": "hi"}, http.StatusNotImplemented, "voice_off"},
		{http.MethodPost, "/api/stt", nil, http.StatusNotImplemented, "stt_off"},
		{http.MethodPost, "/api/image", map[string]string{"prompt": "cat"}, http.StatusNotImplemented, "images_off"},
		{http.MethodGet, "/api/nothing", nil, http.StatusNotFound, "not_found"},
		{http.MethodPost, "/api/status", nil, http.StatusNotFound, "not_found"},
		{http.MethodPost, "/api/turn", map[string]string{"text": "   "}, http.StatusBadRequest, "empty"},
		{http.MethodPost, "/api/runs/none/cancel", nil, http.StatusNotFound, "no_run"},
		{http.MethodPost, "/api/session", map[string]string{"action": "bogus"}, http.StatusBadRequest, "unsupported"},
		{http.MethodPost, "/api/command", "not json", http.StatusBadRequest, "bad_json"},
	}
	for _, p := range probes {
		code, body := call(t, srv, p.method, p.path, p.body, true)
		if code != p.want || !strings.Contains(string(body), p.contains) {
			t.Errorf("%s %s = %d %s; want %d containing %q", p.method, p.path, code, body, p.want, p.contains)
		}
	}
	if resp, body := call(t, srv, http.MethodGet, "/nope", nil, false); resp != http.StatusNotFound || len(body) == 0 {
		t.Errorf("unknown page = %d", resp)
	}
	if o, err := turnOptionsFrom(turnRequest{Images: []struct {
		Name      string `json:"name"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}{{Name: "bad", Data: "%%%"}}}); err == nil || len(o.Images) != 0 {
		t.Errorf("undecodable image must be rejected: %+v %v", o, err)
	}
}
