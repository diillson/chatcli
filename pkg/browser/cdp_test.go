/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * cdp_test.go
 *
 * The wire client against a fake in-process CDP endpoint: command/response
 * matching, error surfacing, event dispatch and shutdown semantics — no real
 * browser involved. Event routing into the session rings is tested pure.
 */
package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startFakeCDP serves a websocket that answers every command via respond and
// can push events through the returned function.
func startFakeCDP(t *testing.T, respond func(msg cdpMessage) cdpMessage) (wsURL string, push func(cdpMessage)) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	events := make(chan cdpMessage, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// One writer mutex: the event pusher and the responder both write
		// to the same socket (gorilla allows at most one concurrent writer).
		var wmu sync.Mutex
		write := func(v cdpMessage) {
			data, _ := json.Marshal(v)
			wmu.Lock()
			_ = ws.WriteMessage(websocket.TextMessage, data)
			wmu.Unlock()
		}
		go func() {
			for ev := range events {
				write(ev)
			}
		}()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var msg cdpMessage
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			resp := respond(msg)
			resp.ID = msg.ID
			write(resp)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), func(ev cdpMessage) { events <- ev }
}

func TestCDPCallRoundTripAndError(t *testing.T) {
	wsURL, _ := startFakeCDP(t, func(msg cdpMessage) cdpMessage {
		if msg.Method == "Boom.fail" {
			return cdpMessage{Error: &cdpError{Code: -32000, Message: "nope"}}
		}
		return cdpMessage{Result: json.RawMessage(`{"ok":true,"method":"` + msg.Method + `"}`)}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialCDP(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()

	res, err := conn.call(ctx, "sess-1", "Page.navigate", map[string]interface{}{"url": "http://x"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(res), "Page.navigate") {
		t.Fatalf("unexpected result: %s", res)
	}

	if _, err := conn.call(ctx, "", "Boom.fail", nil); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected CDP error surfaced, got %v", err)
	}
}

func TestCDPEventsDispatchAndClose(t *testing.T) {
	wsURL, push := startFakeCDP(t, func(cdpMessage) cdpMessage {
		return cdpMessage{Result: json.RawMessage(`{}`)}
	})

	got := make(chan string, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialCDP(ctx, wsURL, func(method string, _ json.RawMessage, sessionID string) {
		got <- method + "|" + sessionID
	})
	if err != nil {
		t.Fatal(err)
	}

	push(cdpMessage{Method: "Page.loadEventFired", SessionID: "s1", Params: json.RawMessage(`{}`)})
	select {
	case ev := <-got:
		if ev != "Page.loadEventFired|s1" {
			t.Fatalf("unexpected event %q", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event not dispatched")
	}

	conn.close()
	if _, err := conn.call(ctx, "", "Page.enable", nil); err == nil {
		t.Fatal("call on closed connection must error")
	}
}

func TestHandleEvent_ConsoleNetworkAndLoad(t *testing.T) {
	s := &Session{
		sessionID: "sess",
		requests:  map[string]NetworkEntry{},
		loadCh:    make(chan struct{}),
	}

	// Foreign-session events are ignored.
	s.handleEvent("Runtime.consoleAPICalled", json.RawMessage(`{"type":"log","args":[{"value":"other"}]}`), "another")
	if len(s.ConsoleTail(0)) != 0 {
		t.Fatal("foreign session event must be ignored")
	}

	s.handleEvent("Runtime.consoleAPICalled",
		json.RawMessage(`{"type":"error","args":[{"value":"boom"},{"description":"at app.js:1"}]}`), "sess")
	s.handleEvent("Runtime.exceptionThrown",
		json.RawMessage(`{"exceptionDetails":{"text":"Uncaught","exception":{"description":"TypeError: x is not a function"}}}`), "sess")

	entries := s.ConsoleTail(0)
	if len(entries) != 2 {
		t.Fatalf("expected 2 console entries, got %d", len(entries))
	}
	if entries[0].Kind != "error" || !strings.Contains(entries[0].Text, "boom") {
		t.Fatalf("unexpected console entry: %+v", entries[0])
	}
	if !strings.Contains(entries[1].Text, "TypeError") {
		t.Fatalf("exception not captured: %+v", entries[1])
	}

	// Network pair: request then response.
	s.handleEvent("Network.requestWillBeSent",
		json.RawMessage(`{"requestId":"r1","type":"XHR","request":{"method":"POST","url":"http://api/x"}}`), "sess")
	s.handleEvent("Network.responseReceived",
		json.RawMessage(`{"requestId":"r1","type":"XHR","response":{"status":500,"url":"http://api/x"}}`), "sess")
	net := s.NetworkTail(0)
	if len(net) != 1 || net[0].Status != 500 || net[0].Method != "POST" || net[0].Type != "XHR" {
		t.Fatalf("network pair not captured: %+v", net)
	}

	// Load event closes the channel exactly once (idempotent).
	s.handleEvent("Page.loadEventFired", nil, "sess")
	s.handleEvent("Page.loadEventFired", nil, "sess")
	select {
	case <-s.loadCh:
	default:
		t.Fatal("load channel must be closed")
	}
}

func TestRingBounds(t *testing.T) {
	s := &Session{sessionID: "sess", requests: map[string]NetworkEntry{}, loadCh: make(chan struct{})}
	for i := 0; i < maxRing+50; i++ {
		s.appendConsole(ConsoleEntry{Kind: "log", Text: "x"})
	}
	if got := len(s.ConsoleTail(0)); got != maxRing {
		t.Fatalf("console ring must cap at %d, got %d", maxRing, got)
	}
	if got := len(s.ConsoleTail(5)); got != 5 {
		t.Fatalf("tail(5) must return 5, got %d", got)
	}
}

func TestChromeCandidatesNonEmpty(t *testing.T) {
	if len(chromeCandidates()) == 0 {
		t.Fatal("candidate list must never be empty")
	}
}

// fakeSession wires a Session to an in-process fake CDP endpoint so every
// action (navigate, snapshot, click, type, eval, screenshot, scroll) is
// exercised deterministically without a real browser.
func fakeSession(t *testing.T) (*Session, func(cdpMessage)) {
	t.Helper()
	var pushRef func(cdpMessage)
	wsURL, push := startFakeCDP(t, func(msg cdpMessage) cdpMessage {
		switch msg.Method {
		case "Page.navigate":
			// Announce the load event right after the navigation command.
			go pushRef(cdpMessage{Method: "Page.loadEventFired", SessionID: "s1", Params: json.RawMessage(`{}`)})
			return cdpMessage{Result: json.RawMessage(`{"frameId":"f1"}`)}
		case "Page.captureScreenshot":
			// Base64 of a minimal PNG header.
			return cdpMessage{Result: json.RawMessage(`{"data":"iVBORw0KGgo="}`)}
		case "Runtime.evaluate":
			var p struct {
				Expression string `json:"expression"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			switch {
			case strings.Contains(p.Expression, "interactiveSel"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"title\":\"Fake Page\",\"url\":\"http://fake/\",\"interactive\":[\"[1] <a> Home -> /\"],\"text\":\"Hello world\"}"}}`)}
			case strings.Contains(p.Expression, "location.href"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"t\":\"Fake Page\",\"u\":\"http://fake/\"}"}}`)}
			case strings.Contains(p.Expression, "NOTFOUND"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"OK"}}`)}
			case strings.Contains(p.Expression, "throw"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"object"},"exceptionDetails":{"text":"Uncaught","exception":{"description":"ReferenceError: nope"}}}`)}
			default:
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"number","value":42}}`)}
			}
		}
		return cdpMessage{Result: json.RawMessage(`{}`)}
	})
	pushRef = push

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	s := &Session{sessionID: "s1", requests: map[string]NetworkEntry{}, loadCh: make(chan struct{})}
	conn, err := dialCDP(ctx, wsURL, s.handleEvent)
	if err != nil {
		t.Fatal(err)
	}
	s.conn = conn
	t.Cleanup(conn.close)
	return s, push
}

func TestSessionActionsAgainstFakeCDP(t *testing.T) {
	s, _ := fakeSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	title, url, err := s.Navigate(ctx, "http://fake/")
	if err != nil || title != "Fake Page" || url != "http://fake/" {
		t.Fatalf("navigate: %q %q %v", title, url, err)
	}

	snap, err := s.Snapshot(ctx, 5)
	if err != nil || !strings.Contains(snap, "Fake Page") || !strings.Contains(snap, "[1] <a> Home") {
		t.Fatalf("snapshot: %q %v", snap, err)
	}
	if !strings.Contains(snap, "truncated") {
		t.Fatalf("maxBytes=5 must truncate page text: %q", snap)
	}

	if err := s.Click(ctx, "1"); err != nil {
		t.Fatalf("click: %v", err)
	}
	if err := s.Type(ctx, "#q", "golang", true); err != nil {
		t.Fatalf("type: %v", err)
	}
	if err := s.Scroll(ctx, "down", ""); err != nil {
		t.Fatalf("scroll: %v", err)
	}
	if err := s.Scroll(ctx, "", "#footer"); err != nil {
		t.Fatalf("scroll to: %v", err)
	}

	out, err := s.Eval(ctx, "6*7")
	if err != nil || out != "42" {
		t.Fatalf("eval: %q %v", out, err)
	}
	if _, err := s.Eval(ctx, "throw new Error('nope')"); err == nil || !strings.Contains(err.Error(), "ReferenceError") {
		t.Fatalf("page JS error must surface: %v", err)
	}

	shot := t.TempDir() + "/s.png"
	if err := s.Screenshot(ctx, shot); err != nil {
		t.Fatalf("screenshot: %v", err)
	}

	title, url, err = s.Identity(ctx)
	if err != nil || title != "Fake Page" || url != "http://fake/" {
		t.Fatalf("identity: %q %q %v", title, url, err)
	}

	title, _, err = s.Back(ctx)
	if err != nil || title != "Fake Page" {
		t.Fatalf("back: %q %v", title, err)
	}

	if !s.Alive() {
		t.Fatal("session with live conn must report alive")
	}
}

func TestWaitForDevTools(t *testing.T) {
	ctx := context.Background()
	r := strings.NewReader("some noise\nDevTools listening on ws://127.0.0.1:9222/devtools/browser/abc\nmore\n")
	url, err := waitForDevTools(ctx, r)
	if err != nil || url != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Fatalf("got %q, %v", url, err)
	}
	if _, err := waitForDevTools(ctx, strings.NewReader("crashed without endpoint\n")); err == nil {
		t.Fatal("EOF without marker must error")
	}
}
