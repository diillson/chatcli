/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package browser

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// extFakeSession wires a Session to a fake CDP endpoint that records every
// method (with params) and answers the second-tier verbs deterministically.
func extFakeSession(t *testing.T) (*Session, func() []cdpMessage, func(cdpMessage)) {
	t.Helper()
	var mu sync.Mutex
	var seen []cdpMessage
	wsURL, push := startFakeCDP(t, func(msg cdpMessage) cdpMessage {
		mu.Lock()
		seen = append(seen, msg)
		mu.Unlock()
		switch msg.Method {
		case "Runtime.evaluate":
			var p struct {
				Expression string `json:"expression"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			switch {
			case strings.Contains(p.Expression, "getBoundingClientRect"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"x\":10,\"y\":20}"}}`)}
			case strings.Contains(p.Expression, "NOOPTION"):
				if strings.Contains(p.Expression, `"missing"`) {
					return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"NOOPTION:br=Brazil | us=USA"}}`)}
				}
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"OK:br"}}`)}
			case strings.Contains(p.Expression, "outerHTML"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"<html><body>hello</body></html>"}}`)}
			}
			return cdpMessage{Result: json.RawMessage(`{"result":{"type":"number","value":1}}`)}
		case "DOM.getDocument":
			return cdpMessage{Result: json.RawMessage(`{"root":{"nodeId":1}}`)}
		case "DOM.querySelector":
			var p struct {
				Selector string `json:"selector"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Selector == "#missing" {
				return cdpMessage{Result: json.RawMessage(`{"nodeId":0}`)}
			}
			return cdpMessage{Result: json.RawMessage(`{"nodeId":7}`)}
		case "Page.printToPDF":
			return cdpMessage{Result: json.RawMessage(`{"data":"JVBERi0xLjQK"}`)}
		case "Page.getLayoutMetrics":
			return cdpMessage{Result: json.RawMessage(`{"cssContentSize":{"width":800,"height":3000}}`)}
		case "Page.captureScreenshot":
			return cdpMessage{Result: json.RawMessage(`{"data":"iVBORw0KGgo="}`)}
		case "Target.getTargets":
			return cdpMessage{Result: json.RawMessage(`{"targetInfos":[
				{"targetId":"t1","type":"page","title":"App","url":"https://app/"},
				{"targetId":"bg","type":"service_worker","title":"sw","url":"https://app/sw.js"},
				{"targetId":"t2","type":"page","title":"OAuth","url":"https://idp/consent"}]}`)}
		case "Target.attachToTarget":
			return cdpMessage{Result: json.RawMessage(`{"sessionId":"s2"}`)}
		case "Storage.getCookies":
			return cdpMessage{Result: json.RawMessage(`{"cookies":[
				{"name":"sid","value":"SECRET","domain":".app","path":"/","expires":4102444800,"httpOnly":true,"secure":true},
				{"name":"pref","value":"x","domain":"other.example","path":"/","expires":-1}]}`)}
		}
		return cdpMessage{Result: json.RawMessage(`{}`)}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	s := &Session{sessionID: "s1", targetID: "t1", requests: map[string]NetworkEntry{}, loadCh: make(chan struct{})}
	conn, err := dialCDP(ctx, wsURL, s.handleEvent)
	if err != nil {
		t.Fatal(err)
	}
	s.conn = conn
	t.Cleanup(conn.close)
	return s, func() []cdpMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([]cdpMessage(nil), seen...)
	}, push
}

func methodParams(t *testing.T, seen []cdpMessage, method string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, m := range seen {
		if m.Method == method {
			var p map[string]interface{}
			_ = json.Unmarshal(m.Params, &p)
			out = append(out, p)
		}
	}
	return out
}

func TestPress_KeyEventsAndChords(t *testing.T) {
	s, seen, _ := extFakeSession(t)
	ctx := context.Background()

	if err := s.Press(ctx, "Enter"); err != nil {
		t.Fatal(err)
	}
	evs := methodParams(t, seen(), "Input.dispatchKeyEvent")
	if len(evs) != 2 || evs[0]["type"] != "keyDown" || evs[1]["type"] != "keyUp" || evs[0]["key"] != "Enter" || evs[0]["text"] != "\r" {
		t.Fatalf("enter events: %v", evs)
	}

	if err := s.Press(ctx, "Control+a"); err != nil {
		t.Fatal(err)
	}
	evs = methodParams(t, seen(), "Input.dispatchKeyEvent")
	last := evs[len(evs)-2]
	if last["modifiers"].(float64) != 2 || last["key"] != "a" {
		t.Fatalf("chord events: %v", last)
	}
	if _, has := last["text"]; has {
		t.Fatalf("ctrl chord must not carry text: %v", last)
	}

	if err := s.Press(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	evs = methodParams(t, seen(), "Input.dispatchKeyEvent")
	if evs[len(evs)-2]["text"] != "x" {
		t.Fatalf("printable key must carry text: %v", evs[len(evs)-2])
	}

	for _, bad := range []string{"", "Hyper+a", "Bogus"} {
		if err := s.Press(ctx, bad); err == nil {
			t.Fatalf("Press(%q) must error", bad)
		}
	}
}

func TestHoverSelectUploadHTML(t *testing.T) {
	s, seen, _ := extFakeSession(t)
	ctx := context.Background()

	if err := s.Hover(ctx, "3"); err != nil {
		t.Fatal(err)
	}
	mv := methodParams(t, seen(), "Input.dispatchMouseEvent")
	if len(mv) != 1 || mv[0]["type"] != "mouseMoved" || mv[0]["x"].(float64) != 10 || mv[0]["y"].(float64) != 20 {
		t.Fatalf("hover mouse event: %v", mv)
	}

	if err := s.Select(ctx, "4", "Brazil"); err != nil {
		t.Fatal(err)
	}
	if err := s.Select(ctx, "4", "missing"); err == nil || !strings.Contains(err.Error(), "br=Brazil") {
		t.Fatalf("select must list available options, got %v", err)
	}

	f := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Upload(ctx, "#file", []string{f}); err != nil {
		t.Fatal(err)
	}
	up := methodParams(t, seen(), "DOM.setFileInputFiles")
	if len(up) != 1 || up[0]["nodeId"].(float64) != 7 {
		t.Fatalf("upload: %v", up)
	}
	if err := s.Upload(ctx, "#file", []string{filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("upload of a missing file must error before touching the page")
	}
	if err := s.Upload(ctx, "#missing", []string{f}); err == nil {
		t.Fatal("upload to a missing element must error")
	}

	html, err := s.HTML(ctx, "", 10)
	if err != nil || !strings.HasPrefix(html, "<html><ho") && !strings.Contains(html, "truncated") {
		t.Fatalf("html: %q %v", html, err)
	}
}

func TestPDFResizeScreenshotFull(t *testing.T) {
	s, seen, _ := extFakeSession(t)
	ctx := context.Background()

	pdf := filepath.Join(t.TempDir(), "out", "page.pdf")
	if err := s.PDF(ctx, pdf); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(pdf); err != nil || !strings.HasPrefix(string(data), "%PDF") {
		t.Fatalf("pdf file: %q %v", data, err)
	}

	if err := s.Resize(ctx, 390, 844, true); err != nil {
		t.Fatal(err)
	}
	em := methodParams(t, seen(), "Emulation.setDeviceMetricsOverride")
	if len(em) != 1 || em[0]["width"].(float64) != 390 || em[0]["mobile"] != true || em[0]["deviceScaleFactor"].(float64) != 2 {
		t.Fatalf("resize: %v", em)
	}
	if err := s.Resize(ctx, 0, 10, false); err == nil {
		t.Fatal("zero width must error")
	}

	shot := filepath.Join(t.TempDir(), "full.png")
	if err := s.ScreenshotFull(ctx, shot); err != nil {
		t.Fatal(err)
	}
	cap := methodParams(t, seen(), "Page.captureScreenshot")
	clip, ok := cap[len(cap)-1]["clip"].(map[string]interface{})
	if !ok || clip["height"].(float64) != 3000 || cap[len(cap)-1]["captureBeyondViewport"] != true {
		t.Fatalf("full screenshot params: %v", cap)
	}
}

func TestTabsSwitchAndCookies(t *testing.T) {
	s, seen, _ := extFakeSession(t)
	ctx := context.Background()

	tabs, err := s.Tabs(ctx)
	if err != nil || len(tabs) != 2 || !tabs[0].Current || tabs[1].Current || tabs[1].Title != "OAuth" {
		t.Fatalf("tabs: %+v %v", tabs, err)
	}
	tab, err := s.SwitchTab(ctx, "2")
	if err != nil || tab.ID != "t2" || !tab.Current {
		t.Fatalf("switch: %+v %v", tab, err)
	}
	if s.sessionID != "s2" || s.targetID != "t2" {
		t.Fatalf("session must now drive t2/s2, got %s/%s", s.targetID, s.sessionID)
	}
	enabled := 0
	for _, m := range seen() {
		if m.SessionID == "s2" && strings.HasSuffix(m.Method, ".enable") {
			enabled++
		}
	}
	if enabled != 3 {
		t.Fatalf("Page/Runtime/Network must be enabled on the new session, got %d", enabled)
	}
	if len(methodParams(t, seen(), "Target.activateTarget")) != 1 {
		t.Fatal("switching must bring the tab to the front")
	}
	if _, err := s.SwitchTab(ctx, "9"); err == nil {
		t.Fatal("out-of-range tab must error")
	}
	if _, err := s.SwitchTab(ctx, "nope"); err == nil {
		t.Fatal("unknown tab id must error")
	}

	cookies, err := s.Cookies(ctx, "")
	if err != nil || len(cookies) != 2 {
		t.Fatalf("cookies: %+v %v", cookies, err)
	}
	if cookies[0].Name != "sid" || !cookies[0].HTTPOnly || cookies[0].Expires.IsZero() || !cookies[1].Expires.IsZero() {
		t.Fatalf("cookie fields: %+v", cookies)
	}
	filtered, err := s.Cookies(ctx, "APP")
	if err != nil || len(filtered) != 1 || filtered[0].Domain != ".app" {
		t.Fatalf("filtered cookies: %+v %v", filtered, err)
	}
	if err := s.ClearCookies(ctx); err != nil {
		t.Fatal(err)
	}
	if len(methodParams(t, seen(), "Storage.clearCookies")) != 1 {
		t.Fatal("clear must call Storage.clearCookies")
	}
}

func TestHandleEvent_DialogAutoAccepted(t *testing.T) {
	s, seen, push := extFakeSession(t)
	push(cdpMessage{Method: "Page.javascriptDialogOpening", SessionID: "s1",
		Params: json.RawMessage(`{"type":"confirm","message":"Delete everything?","defaultPrompt":""}`)})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(methodParams(t, seen(), "Page.handleJavaScriptDialog")) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	handled := methodParams(t, seen(), "Page.handleJavaScriptDialog")
	if len(handled) != 1 || handled[0]["accept"] != true {
		t.Fatalf("dialog must be accepted: %v", handled)
	}
	tail := s.ConsoleTail(0)
	if len(tail) != 1 || tail[0].Kind != "dialog" || !strings.Contains(tail[0].Text, "Delete everything?") {
		t.Fatalf("dialog must be traced in the console ring: %+v", tail)
	}
}

func TestPersistentProfileDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(ProfileEnv, "")
	if got := persistentProfileDir(); got != "" {
		t.Fatalf("unset must be throwaway, got %q", got)
	}
	for _, off := range []string{"0", "false", "none", "ephemeral"} {
		t.Setenv(ProfileEnv, off)
		if got := persistentProfileDir(); got != "" {
			t.Fatalf("%q must be throwaway, got %q", off, got)
		}
	}
	for _, on := range []string{"1", "true", "persistent"} {
		t.Setenv(ProfileEnv, on)
		want := filepath.Join(home, ".chatcli", "browser", "profile")
		if got := persistentProfileDir(); got != want {
			t.Fatalf("%q: got %q want %q", on, got, want)
		}
	}
	t.Setenv(ProfileEnv, "~/custom/prof")
	if got := persistentProfileDir(); got != filepath.Join(home, "custom", "prof") {
		t.Fatalf("tilde expansion: %q", got)
	}
	explicit := filepath.Join(t.TempDir(), "p")
	t.Setenv(ProfileEnv, explicit)
	if got := persistentProfileDir(); got != explicit {
		t.Fatalf("explicit path: %q", got)
	}
}

func TestResolveDevToolsWS(t *testing.T) {
	ctx := context.Background()
	if ws, err := resolveDevToolsWS(ctx, "ws://127.0.0.1:9222/devtools/browser/x"); err != nil || !strings.HasPrefix(ws, "ws://") {
		t.Fatalf("ws passthrough: %q %v", ws, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"Browser":"Chrome/140","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/abc"}`))
	}))
	t.Cleanup(srv.Close)
	ws, err := resolveDevToolsWS(ctx, srv.URL+"/")
	if err != nil || ws != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Fatalf("http resolve: %q %v", ws, err)
	}
	// Bare host:port gets the scheme filled in.
	if ws, err := resolveDevToolsWS(ctx, strings.TrimPrefix(srv.URL, "http://")); err != nil || ws == "" {
		t.Fatalf("bare host: %q %v", ws, err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	t.Cleanup(bad.Close)
	if _, err := resolveDevToolsWS(ctx, bad.URL); err == nil {
		t.Fatal("missing webSocketDebuggerUrl must error")
	}
	if _, err := resolveDevToolsWS(ctx, "http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "--remote-debugging-port") {
		t.Fatalf("unreachable endpoint must hint at the flag, got %v", err)
	}
}

func TestAttachSession_OwnsOnlyItsTab(t *testing.T) {
	var mu sync.Mutex
	var seen []cdpMessage
	wsURL, _ := startFakeCDP(t, func(msg cdpMessage) cdpMessage {
		mu.Lock()
		seen = append(seen, msg)
		mu.Unlock()
		switch msg.Method {
		case "Target.createTarget":
			return cdpMessage{Result: json.RawMessage(`{"targetId":"mine"}`)}
		case "Target.attachToTarget":
			return cdpMessage{Result: json.RawMessage(`{"sessionId":"sx"}`)}
		}
		return cdpMessage{Result: json.RawMessage(`{}`)}
	})
	t.Setenv(CDPURLEnv, wsURL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s, err := NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Attached() || !s.Visible() || s.ProfileDir() != "" || s.cmd != nil {
		t.Fatalf("attached session shape wrong: attached=%t visible=%t dir=%q", s.Attached(), s.Visible(), s.ProfileDir())
	}
	s.Close(ctx)
	mu.Lock()
	defer mu.Unlock()
	var closedTarget, browserClose bool
	for _, m := range seen {
		switch m.Method {
		case "Target.closeTarget":
			closedTarget = strings.Contains(string(m.Params), `"mine"`)
		case "Browser.close":
			browserClose = true
		}
	}
	if !closedTarget || browserClose {
		t.Fatalf("attached Close must close only its tab (closeTarget=%t browserClose=%t)", closedTarget, browserClose)
	}
	if s.Alive() {
		t.Fatal("closed attached session must not report alive")
	}
}

func TestAcquireMode_AttachedIgnoresVisibilityFlips(t *testing.T) {
	wsURL, _ := startFakeCDP(t, func(msg cdpMessage) cdpMessage {
		switch msg.Method {
		case "Target.createTarget":
			return cdpMessage{Result: json.RawMessage(`{"targetId":"mine"}`)}
		case "Target.attachToTarget":
			return cdpMessage{Result: json.RawMessage(`{"sessionId":"sx"}`)}
		}
		return cdpMessage{Result: json.RawMessage(`{}`)}
	})
	t.Setenv(CDPURLEnv, wsURL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() { Shutdown(context.Background()) })

	first, err := AcquireMode(ctx, ModeVisible)
	if err != nil {
		t.Fatal(err)
	}
	again, err := AcquireMode(ctx, ModeHeadless)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatal("an attached session must never be relaunched for a visibility change")
	}
	if !DefaultAttached() || !DefaultVisible() {
		t.Fatal("default status must report attached+visible")
	}
	if dir, persistent := DefaultProfile(); dir != "" || persistent {
		t.Fatal("attached session has no profile of its own")
	}
}
