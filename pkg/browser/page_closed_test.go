/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChromeArgs_AutomationControlledOffAndHeadlessFirst(t *testing.T) {
	args := chromeArgs(true, "/tmp/p")
	if args[0] != "--headless=new" {
		t.Fatalf("headless flag must lead: %v", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--disable-blink-features=AutomationControlled", "--user-data-dir=/tmp/p", "--remote-debugging-port=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %v", want, args)
		}
	}
	if strings.Contains(strings.Join(chromeArgs(false, "/tmp/p"), " "), "--headless") {
		t.Fatal("visible launch must not pass --headless")
	}
}

// closedPageSession wires a Session to a fake browser whose page session
// can be "closed": once gone, page-scoped commands answer -32001 until a
// new target is created.
func closedPageSession(t *testing.T) (*Session, *atomic.Int32, *atomic.Bool, func(cdpMessage)) {
	t.Helper()
	var created atomic.Int32
	var gone atomic.Bool
	var mu sync.Mutex
	live := map[string]bool{"s1": true}
	wsURL, push := startFakeCDP(t, func(msg cdpMessage) cdpMessage {
		switch msg.Method {
		case "Target.createTarget":
			n := created.Add(1)
			return cdpMessage{Result: json.RawMessage(`{"targetId":"t` + strconv.Itoa(int(n)) + `"}`)}
		case "Target.attachToTarget":
			sid := "s" + strconv.Itoa(int(created.Load()))
			mu.Lock()
			live[sid] = true
			mu.Unlock()
			gone.Store(false)
			return cdpMessage{Result: json.RawMessage(`{"sessionId":"` + sid + `"}`)}
		}
		if msg.SessionID != "" {
			mu.Lock()
			ok := live[msg.SessionID]
			mu.Unlock()
			if !ok || gone.Load() {
				return cdpMessage{Error: &cdpError{Code: -32001, Message: "Session with given id not found."}}
			}
		}
		if msg.Method == "Runtime.evaluate" {
			return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"t\":\"T\",\"u\":\"http://fake/\"}"}}`)}
		}
		return cdpMessage{Result: json.RawMessage(`{}`)}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	s := &Session{requests: map[string]NetworkEntry{}, loadCh: make(chan struct{})}
	conn, err := dialCDP(ctx, wsURL, s.handleEvent)
	if err != nil {
		t.Fatal(err)
	}
	s.conn = conn
	t.Cleanup(conn.close)
	if err := s.attachFreshTarget(ctx); err != nil {
		t.Fatal(err)
	}
	return s, &created, &gone, push
}

func TestCall_SessionNotFoundBecomesErrPageClosedThenReattaches(t *testing.T) {
	s, created, gone, _ := closedPageSession(t)
	ctx := context.Background()
	if _, _, err := s.Identity(ctx); err != nil {
		t.Fatal(err)
	}
	gone.Store(true)
	_, _, err := s.Identity(ctx)
	if !errors.Is(err, ErrPageClosed) {
		t.Fatalf("closed page must surface ErrPageClosed, got %v", err)
	}
	if !s.PageClosed() {
		t.Fatal("PageClosed must report true after the browser answered -32001")
	}
	// The next command reattaches a fresh tab and succeeds.
	title, url, err := s.Identity(ctx)
	if err != nil || title != "T" || url != "http://fake/" {
		t.Fatalf("after reattach: %q %q %v", title, url, err)
	}
	if created.Load() != 2 {
		t.Fatalf("exactly one new target expected, got %d", created.Load())
	}
	if s.PageClosed() || s.sessionID != "s2" || s.targetID != "t2" {
		t.Fatalf("session must now drive the new tab: closed=%t sid=%s tid=%s", s.PageClosed(), s.sessionID, s.targetID)
	}
}

func TestHandleEvent_DetachMarksPageClosed(t *testing.T) {
	s, created, gone, push := closedPageSession(t)
	ctx := context.Background()
	push(cdpMessage{Method: "Target.detachedFromTarget", Params: json.RawMessage(`{"sessionId":"s1","targetId":"t1"}`)})
	deadline := time.Now().Add(2 * time.Second)
	for !s.PageClosed() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.PageClosed() {
		t.Fatal("detachedFromTarget for our session must mark the page closed")
	}
	// The first command after the event REPORTS the closure — a polling
	// wait must see it rather than silently continue on a blank tab.
	gone.Store(true)
	if _, _, err := s.Identity(ctx); !errors.Is(err, ErrPageClosed) {
		t.Fatalf("first call after detach must return ErrPageClosed, got %v", err)
	}
	if created.Load() != 1 {
		t.Fatalf("reporting must not reattach yet, created=%d", created.Load())
	}
	// The command after that reattaches and works.
	if _, _, err := s.Identity(ctx); err != nil {
		t.Fatalf("second call after detach must reattach: %v", err)
	}
	if created.Load() != 2 || s.PageClosed() {
		t.Fatalf("expected a fresh target, created=%d closed=%t", created.Load(), s.PageClosed())
	}
	// An event for another session/target is ignored.
	s.mu.Lock()
	s.targetGone = false
	s.mu.Unlock()
	push(cdpMessage{Method: "Target.targetDestroyed", Params: json.RawMessage(`{"targetId":"other"}`)})
	time.Sleep(50 * time.Millisecond)
	if s.PageClosed() {
		t.Fatal("events for other targets must not mark the page closed")
	}
}

func TestCall_BrowserGoneBecomesErrBrowserClosed(t *testing.T) {
	s, _, gone, _ := closedPageSession(t)
	ctx := context.Background()
	gone.Store(true)
	if _, _, err := s.Identity(ctx); !errors.Is(err, ErrPageClosed) {
		t.Fatalf("want ErrPageClosed first, got %v", err)
	}
	// The browser goes away entirely before the reattach.
	s.conn.close()
	if _, _, err := s.Identity(ctx); !errors.Is(err, ErrBrowserClosed) {
		t.Fatalf("reattach against a dead browser must be ErrBrowserClosed, got %v", err)
	}
}
