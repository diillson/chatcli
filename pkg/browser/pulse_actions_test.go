/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package browser

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

// Every page action is a node under the browser's node; a navigation says
// which host it reached and nothing about the page travels.
func TestPageActionsAreNodesUnderTheBrowser(t *testing.T) {
	rec := pulsetest.Record(t)
	var push func(cdpMessage)
	wsURL, push := startFakeCDP(t, func(msg cdpMessage) cdpMessage {
		switch msg.Method {
		case "Page.navigate":
			push(cdpMessage{Method: "Page.loadEventFired", SessionID: "s1", Params: json.RawMessage(`{}`)})
		case "Runtime.evaluate":
			var p struct {
				Expression string `json:"expression"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			switch {
			case strings.Contains(p.Expression, "markers"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"title\":\"SECRET-TITLE\",\"url\":\"https://docs.example.com/SECRET-PATH\",\"interactive\":[],\"text\":\"\",\"markers\":{}}"}}`)}
			case strings.Contains(p.Expression, "location.href"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"t\":\"SECRET-TITLE\",\"u\":\"https://docs.example.com/SECRET-PATH?token=SECRET\"}"}}`)}
			case strings.Contains(p.Expression, "mode: 'mouse'"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"mode\":\"mouse\",\"x\":5,\"y\":6}"}}`)}
			case strings.Contains(p.Expression, "kind: 'value'"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"kind\":\"value\",\"password\":false}"}}`)}
			}
			return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"OK"}}`)}
		case "Page.captureScreenshot":
			return cdpMessage{Result: json.RawMessage(`{"data":"iVBORw0KGgo="}`)}
		}
		return cdpMessage{Result: json.RawMessage(`{}`)}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := &Session{sessionID: "s1", targetID: "t1", requests: map[string]NetworkEntry{}, loadCh: make(chan struct{})}
	conn, err := dialCDP(ctx, wsURL, s.handleEvent)
	if err != nil {
		t.Fatal(err)
	}
	s.conn = conn
	t.Cleanup(conn.close)
	s.pulseBegin()
	browserNode := s.pulse.ID()

	if _, _, err := s.Navigate(ctx, "https://docs.example.com/SECRET-PATH?token=SECRET"); err != nil {
		t.Fatal(err)
	}
	if err := s.Click(ctx, "#SECRET-SELECTOR"); err != nil {
		t.Fatal(err)
	}
	if err := s.Type(ctx, "[3]", "SECRET-TYPED", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Eval(ctx, "SECRET-SCRIPT"); err != nil {
		t.Fatal(err)
	}
	shot := filepath.Join(t.TempDir(), "SECRET-FILE.png")
	if err := s.Screenshot(ctx, shot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shot); err != nil {
		t.Fatal(err)
	}

	nav := rec.WaitFor(t, "navigate end", pulsetest.Is(pulse.KindTool, "browser:navigate", pulse.PhaseEnd))
	if nav.Parent != browserNode || nav.Attrs["host"] != "docs.example.com" || nav.Attrs["action"] != "navigate" || nav.Status != pulse.StatusOK {
		t.Errorf("navigate = %+v", nav)
	}
	for _, action := range []string{"click", "type", "eval", "screenshot"} {
		ev := rec.WaitFor(t, action+" end", pulsetest.Is(pulse.KindTool, "browser:"+action, pulse.PhaseEnd))
		if ev.Parent != browserNode || ev.Attrs["action"] != action || ev.Status != pulse.StatusOK {
			t.Errorf("%s = %+v", action, ev)
		}
	}
	for _, e := range rec.Events() {
		for k, v := range e.Attrs {
			if strings.Contains(v, "SECRET") || strings.Contains(v, "/") {
				t.Fatalf("page content leaked onto the bus: %s=%q", k, v)
			}
		}
	}
}

// A refused action ends as an error, and with no browser node the action
// hangs from whatever started it.
func TestPageActionFailureAndFallbackParent(t *testing.T) {
	rec := pulsetest.Record(t)
	wsURL, _ := startFakeCDP(t, func(cdpMessage) cdpMessage {
		return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"NOTFOUND"}}`)}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := &Session{sessionID: "s1", requests: map[string]NetworkEntry{}, loadCh: make(chan struct{})}
	conn, err := dialCDP(ctx, wsURL, s.handleEvent)
	if err != nil {
		t.Fatal(err)
	}
	s.conn = conn
	t.Cleanup(conn.close)

	if err := s.Click(pulse.WithParent(ctx, "tool-5"), "#missing"); err == nil {
		t.Fatal("expected not found")
	}
	ev := rec.WaitFor(t, "failed click", pulsetest.Is(pulse.KindTool, "browser:click", pulse.PhaseEnd))
	if ev.Status != pulse.StatusError || ev.Parent != "tool-5" {
		t.Errorf("click = %+v", ev)
	}
}
