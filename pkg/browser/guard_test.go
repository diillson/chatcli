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
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedCall struct {
	method string
	params string
}

// guardedFakeSession is a Session on a fake CDP endpoint that records
// every command it receives.
func guardedFakeSession(t *testing.T) (*Session, func(cdpMessage), func() []recordedCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []recordedCall
	wsURL, push := startFakeCDP(t, func(msg cdpMessage) cdpMessage {
		mu.Lock()
		calls = append(calls, recordedCall{method: msg.Method, params: string(msg.Params)})
		mu.Unlock()
		return cdpMessage{Result: json.RawMessage(`{}`)}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	s := &Session{sessionID: "s1", requests: map[string]NetworkEntry{}, loadCh: make(chan struct{})}
	conn, err := dialCDP(ctx, wsURL, s.handleEvent)
	if err != nil {
		t.Fatal(err)
	}
	s.conn = conn
	t.Cleanup(conn.close)
	snapshot := func() []recordedCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedCall(nil), calls...)
	}
	return s, push, snapshot
}

func installTestGuard(t *testing.T) {
	t.Helper()
	SetNavigationGuard(func(u string) error {
		if strings.HasPrefix(u, "https://") || u == "about:blank" {
			return nil
		}
		return errors.New("blocked by test guard")
	})
	t.Cleanup(func() { SetNavigationGuard(nil) })
}

func TestNavigate_RefusedByGuard(t *testing.T) {
	installTestGuard(t)
	s, _, calls := guardedFakeSession(t)
	if _, _, err := s.Navigate(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("Navigate must refuse a URL the guard rejects")
	}
	for _, c := range calls() {
		if c.method == "Page.navigate" {
			t.Fatal("Page.navigate must not be sent for a refused URL")
		}
	}
}

func TestRequestPaused_GuardDecides(t *testing.T) {
	installTestGuard(t)
	s, push, calls := guardedFakeSession(t)
	_ = s
	push(cdpMessage{Method: "Fetch.requestPaused", SessionID: "s1", Params: json.RawMessage(`{"requestId":"r-bad","request":{"url":"http://169.254.169.254/latest/meta-data/"}}`)})
	push(cdpMessage{Method: "Fetch.requestPaused", SessionID: "other", Params: json.RawMessage(`{"requestId":"r-good","request":{"url":"https://example.test/"}}`)})

	deadline := time.Now().Add(3 * time.Second)
	var failed, continued bool
	for time.Now().Before(deadline) && (!failed || !continued) {
		for _, c := range calls() {
			if c.method == "Fetch.failRequest" && strings.Contains(c.params, "r-bad") && strings.Contains(c.params, "BlockedByClient") {
				failed = true
			}
			if c.method == "Fetch.continueRequest" && strings.Contains(c.params, "r-good") {
				continued = true
			}
			if c.method == "Fetch.continueRequest" && strings.Contains(c.params, "r-bad") {
				t.Fatal("a blocked document request was continued")
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !failed || !continued {
		t.Fatalf("failed=%v continued=%v calls=%v", failed, continued, calls())
	}
}

func TestEnableDocumentGuard_OnlyWithGuard(t *testing.T) {
	s, _, calls := guardedFakeSession(t)
	if err := s.enableDocumentGuard(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls() {
		if c.method == "Fetch.enable" {
			t.Fatal("no guard installed: Fetch must stay off")
		}
	}
	installTestGuard(t)
	if err := s.enableDocumentGuard(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range calls() {
		if c.method == "Fetch.enable" && strings.Contains(c.params, `"resourceType":"Document"`) {
			found = true
		}
	}
	if !found {
		t.Fatal("Fetch.enable with a Document pattern expected")
	}
}
