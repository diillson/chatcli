/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package browser

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDetectChallenge(t *testing.T) {
	cases := []struct {
		name string
		scan pageScan
		want string
	}{
		{"real content, no markers", pageScan{Title: "Go Blog", Text: strings.Repeat("Go is a language. ", 300)}, ""},
		{"cloudflare interstitial by marker", pageScan{Title: "example.com", Text: "Checking your browser", Markers: map[string]bool{"cloudflare": true}}, "Cloudflare"},
		{"cloudflare by title in Portuguese", pageScan{Title: "Verificando se você é humano…", Text: "Revisar a segurança da sua conexão."}, "access check"},
		{"just a moment with marker", pageScan{Title: "Just a moment...", Text: "", Markers: map[string]bool{"cloudflare": true}}, "Cloudflare"},
		{"recaptcha widget on a long real form is not a block", pageScan{Title: "Sign up", Text: strings.Repeat("Create your account. ", 200), Markers: map[string]bool{"recaptcha": true}}, ""},
		{"recaptcha alone on an empty page is a block", pageScan{Title: "Verification", Text: "Please verify", Markers: map[string]bool{"recaptcha": true}}, "reCAPTCHA"},
		{"article quoting the phrase is not a block", pageScan{Title: "How CDNs work", Text: "Sites may say 'checking your browser' before loading. " + strings.Repeat("More prose. ", 400)}, ""},
		{"perimeterx press and hold", pageScan{Title: "Access to this page has been denied", Text: "Press & Hold to confirm you are a human", Markers: map[string]bool{"perimeterx": true}}, "PerimeterX/HUMAN"},
		{"akamai reference", pageScan{Title: "Access Denied", Text: "You don't have permission to access this page. Reference #18.abc"}, "access check"},
		{"nil scan", pageScan{}, ""},
	}
	for _, c := range cases {
		if got := detectChallenge(&c.scan); got != c.want {
			t.Errorf("%s: detectChallenge = %q, want %q", c.name, got, c.want)
		}
	}
	if detectChallenge(nil) != "" {
		t.Fatal("nil scan must not be a challenge")
	}
}

func TestRenderSnapshot_ChallengeMaskAndMore(t *testing.T) {
	scan := &pageScan{
		Title: "Just a moment...", URL: "https://x/",
		Interactive: []string{"[1] <input> Password type=password value=<hidden>"},
		More:        3, CrossFrames: []string{"https://challenges.cloudflare.com/turnstile"},
		Text:    "Verify you are human",
		Markers: map[string]bool{"cloudflare": true},
	}
	out := renderSnapshot(scan, 0, 0)
	for _, want := range []string{"Bot challenge detected (Cloudflare)", "wait --unblocked", "3 more interactive element", "Cross-origin iframe", "turnstile", "value=<hidden>", "Page text:\nVerify you are human"} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot missing %q:\n%s", want, out)
		}
	}
}

func TestTextWindowAndMatches(t *testing.T) {
	text := "héllo wörld, hello again, HELLO at the end"
	w, start, end, total := textWindow(text, 0, 5)
	if w != "héll" && w != "héllo"[:5] {
		t.Fatalf("window = %q", w)
	}
	if start != 0 || end != len(w) || total != len(text) {
		t.Fatalf("range = %d-%d/%d", start, end, total)
	}
	// An offset inside a multibyte rune moves forward to the rune start.
	w, start, _, _ = textWindow(text, 2, 10)
	if start != 3 || !strings.HasPrefix(w, "llo") {
		t.Fatalf("offset mid-rune: start=%d w=%q", start, w)
	}
	w, _, end, _ = textWindow(text, 1000, 10)
	if w != "" || end != total {
		t.Fatalf("offset past the end: %q %d", w, end)
	}

	snips, n := textMatches(text, "hello", 1)
	if n != 2 || len(snips) != 1 {
		t.Fatalf("matches = %d snippets of %d", len(snips), n)
	}
	if !strings.Contains(snips[0], "[hello]") || !strings.Contains(snips[0], "(at byte 15)") {
		t.Fatalf("snippet = %q", snips[0])
	}
	if _, n := textMatches(text, "zzz", 5); n != 0 {
		t.Fatal("no match expected")
	}
}

func TestAcceptLanguage(t *testing.T) {
	for _, key := range []string{"CHATCLI_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(key, "")
	}
	if got := acceptLanguage(); got != "en-US,en;q=0.9" {
		t.Fatalf("default = %q", got)
	}
	t.Setenv("LANG", "pt_BR.UTF-8")
	if got := acceptLanguage(); got != "pt-BR,pt;q=0.9,en-US;q=0.8,en;q=0.7" {
		t.Fatalf("LANG pt_BR = %q", got)
	}
	t.Setenv("CHATCLI_LANG", "en-US")
	if got := acceptLanguage(); got != "en-US,en;q=0.9" {
		t.Fatalf("CHATCLI_LANG wins = %q", got)
	}
	t.Setenv("CHATCLI_LANG", "C")
	t.Setenv("LANG", "de_DE@euro")
	if got := acceptLanguage(); got != "de-DE,de;q=0.9,en-US;q=0.8,en;q=0.7" {
		t.Fatalf("C skipped, modifier stripped = %q", got)
	}
}

func TestChromeArgs_HeadlessIdentity(t *testing.T) {
	args := strings.Join(chromeArgs(true, "/tmp/p"), " ")
	for _, want := range []string{"--headless=new", "--window-size=" + headlessWindowSize, "--accept-lang="} {
		if !strings.Contains(args, want) {
			t.Fatalf("headless args missing %q: %s", want, args)
		}
	}
	if strings.Contains(strings.Join(chromeArgs(false, "/tmp/p"), " "), "--window-size") {
		t.Fatal("a visible window keeps the user's size")
	}
}

func TestNetworkQuietAndLoadingFailed(t *testing.T) {
	s := &Session{requests: map[string]NetworkEntry{}}
	if !s.NetworkQuiet(0) {
		t.Fatal("fresh session is quiet")
	}
	s.handleEvent("Network.requestWillBeSent", json.RawMessage(`{"requestId":"r1","type":"XHR","request":{"method":"GET","url":"https://x/api"}}`), "")
	if s.NetworkQuiet(0) {
		t.Fatal("a request in flight is not quiet")
	}
	s.handleEvent("Network.loadingFailed", json.RawMessage(`{"requestId":"r1","errorText":"net::ERR_BLOCKED_BY_CLIENT"}`), "")
	if !s.NetworkQuiet(0) {
		t.Fatal("a failed request leaves the pending set")
	}
	if s.NetworkQuiet(time.Hour) {
		t.Fatal("activity a moment ago is not an hour of quiet")
	}
	tail := s.NetworkTail(1)
	if len(tail) != 1 || tail[0].Status != 0 || !strings.Contains(tail[0].Type, "ERR_BLOCKED_BY_CLIENT") {
		t.Fatalf("failed request listed = %+v", tail)
	}
	// A canceled request (navigation away) is silently dropped.
	s.handleEvent("Network.requestWillBeSent", json.RawMessage(`{"requestId":"r2","type":"Fetch","request":{"method":"POST","url":"https://x/save"}}`), "")
	s.handleEvent("Network.loadingFailed", json.RawMessage(`{"requestId":"r2","errorText":"net::ERR_ABORTED","canceled":true}`), "")
	if len(s.NetworkTail(0)) != 1 {
		t.Fatal("canceled request must not be listed")
	}
}

// bodyFakeSession answers Network.getResponseBody and the page scripts
// the new verbs run.
func bodyFakeSession(t *testing.T) (*Session, func() []cdpMessage) {
	t.Helper()
	var mu sync.Mutex
	var seen []cdpMessage
	var push func(cdpMessage)
	wsURL, p := startFakeCDP(t, func(msg cdpMessage) cdpMessage {
		mu.Lock()
		seen = append(seen, msg)
		mu.Unlock()
		switch msg.Method {
		case "Page.reload":
			go push(cdpMessage{Method: "Page.loadEventFired", SessionID: "s1", Params: json.RawMessage(`{}`)})
			return cdpMessage{Result: json.RawMessage(`{}`)}
		case "Runtime.evaluate":
			var p struct {
				Expression string `json:"expression"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			switch {
			case strings.Contains(p.Expression, "markers"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"title\":\"Fake\",\"url\":\"http://fake/\",\"interactive\":[\"[1] <button> Save\",\"[2] <a> Help -> /help\"],\"more\":0,\"matches\":[\"[2] <a> Help -> /help\"],\"text\":\"Need help? Help is here. Save your work.\",\"markers\":{}}"}}`)}
			case strings.Contains(p.Expression, "location.href"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"t\":\"Fake\",\"u\":\"http://fake/\"}"}}`)}
			case strings.Contains(p.Expression, "mode: 'mouse'"):
				if strings.Contains(p.Expression, `"#covered"`) {
					return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"mode\":\"js\",\"cover\":\"<div.overlay>\"}"}}`)}
				}
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"mode\":\"mouse\",\"x\":40,\"y\":50}"}}`)}
			case strings.Contains(p.Expression, "kind: 'value'"):
				if strings.Contains(p.Expression, `"#editor"`) {
					return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"kind\":\"editable\",\"password\":false}"}}`)}
				}
				if strings.Contains(p.Expression, `"#pw"`) {
					return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"kind\":\"value\",\"password\":true}"}}`)}
				}
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"{\"kind\":\"value\",\"password\":false}"}}`)}
			case strings.Contains(p.Expression, "NOTEDITABLE"):
				if strings.Contains(p.Expression, `"#sel"`) {
					return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"NOTEDITABLE"}}`)}
				}
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"OK"}}`)}
			case strings.Contains(p.Expression, "el.innerText || el.textContent"):
				return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"OKsection text only"}}`)}
			}
			return cdpMessage{Result: json.RawMessage(`{"result":{"type":"string","value":"OK"}}`)}
		}
		return cdpMessage{Result: json.RawMessage(`{}`)}
	})
	push = p
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
	}
}

func TestClickTypeClearFindTextForwardReload(t *testing.T) {
	s, seen := bodyFakeSession(t)
	ctx := context.Background()

	note, err := s.ClickWith(ctx, "#save", ClickOptions{Count: 2})
	if err != nil || note != "" {
		t.Fatalf("double click: %q %v", note, err)
	}
	mouse := methodParams(t, seen(), "Input.dispatchMouseEvent")
	if len(mouse) != 5 || mouse[0]["type"] != "mouseMoved" || mouse[1]["clickCount"].(float64) != 1 || mouse[4]["clickCount"].(float64) != 2 || mouse[4]["button"] != "left" {
		t.Fatalf("mouse events = %v", mouse)
	}
	if _, err := s.ClickWith(ctx, "#save", ClickOptions{Button: "right"}); err != nil {
		t.Fatal(err)
	}
	if last := methodParams(t, seen(), "Input.dispatchMouseEvent"); last[len(last)-1]["button"] != "right" {
		t.Fatalf("right button: %v", last[len(last)-1])
	}
	if _, err := s.ClickWith(ctx, "#save", ClickOptions{Button: "sideways"}); err == nil {
		t.Fatal("unknown button must error")
	}
	note, err = s.ClickWith(ctx, "#covered", ClickOptions{})
	if err != nil || !strings.Contains(note, "programmatically") || !strings.Contains(note, "<div.overlay>") {
		t.Fatalf("covered click: %q %v", note, err)
	}
	if _, err := s.ClickWith(ctx, "#covered", ClickOptions{Count: 3}); err == nil || !strings.Contains(err.Error(), "triple") {
		t.Fatalf("covered triple click must explain: %v", err)
	}

	pw, err := s.TypeWith(ctx, "#pw", "hunter2", false, false)
	if err != nil || !pw {
		t.Fatalf("password field must be reported: %v %v", pw, err)
	}
	before := len(methodParams(t, seen(), "Input.insertText"))
	pw, err = s.TypeWith(ctx, "#editor", "hello editor", false, true)
	if err != nil || pw {
		t.Fatal(err)
	}
	ins := methodParams(t, seen(), "Input.insertText")
	if len(ins) != before+1 || ins[len(ins)-1]["text"] != "hello editor" {
		t.Fatalf("editable region must receive insertText: %v", ins)
	}
	keys := methodParams(t, seen(), "Input.dispatchKeyEvent")
	if len(keys) < 2 || keys[len(keys)-2]["key"] != "Enter" {
		t.Fatalf("submit on an editable presses Enter: %v", keys)
	}

	if err := s.Clear(ctx, "#q"); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(ctx, "#sel"); err == nil || !strings.Contains(err.Error(), "<select>") {
		t.Fatalf("clear on a select: %v", err)
	}

	out, err := s.Find(ctx, "help", 5)
	if err != nil || !strings.Contains(out, "[2] <a> Help") || !strings.Contains(out, "[help]") || !strings.Contains(out, "Text matches for \"help\" (2 shown of 2)") {
		t.Fatalf("find: %q %v", out, err)
	}
	if _, err := s.Find(ctx, "  ", 5); err == nil {
		t.Fatal("empty find must error")
	}
	out, err = s.Text(ctx, "", 0, 9)
	if err != nil || !strings.HasPrefix(out, "Need help") || !strings.Contains(out, "`--offset 9` continues") {
		t.Fatalf("text paging: %q %v", out, err)
	}
	out, err = s.Text(ctx, "#main", 0, 0)
	if err != nil || !strings.HasPrefix(out, "section text only") || !strings.Contains(out, "end of page") {
		t.Fatalf("text of element: %q %v", out, err)
	}
	if title, _, err := s.Forward(ctx); err != nil || title != "Fake" {
		t.Fatalf("forward: %q %v", title, err)
	}
	if title, _, err := s.Reload(ctx); err != nil || title != "Fake" {
		t.Fatalf("reload: %q %v", title, err)
	}
	if ch, err := s.Challenge(ctx); err != nil || ch != "" {
		t.Fatalf("challenge on plain page: %q %v", ch, err)
	}
	if s.Ephemeral() {
		t.Fatal("a session without a profile dir is not ephemeral")
	}
}
