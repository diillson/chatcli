/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_browser_test.go
 *
 * @browser plugin surface: lenient invocation parsing (JSON envelope and
 * flat argv), dispatch against a fake backend, and the read-only capability
 * split that decides which commands go through the security gate.
 */
package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/browser"
)

type fakeBrowserBackend struct {
	lastOp     string
	lastTarget string
	lastText   string
	lastSubmit bool
	lastJS     string
	console    []browser.ConsoleEntry
	network    []browser.NetworkEntry
	// identity sequence for wait: each Identity call pops the next URL.
	urls    []string
	visible bool
	files   []string
	dims    [2]int
	mobile  bool
	tabs    []browser.TabInfo
	cookies []browser.CookieInfo
	evalOut string
	// identityErr, when set, is returned by Identity (and cleared).
	identityErr error
}

func (f *fakeBrowserBackend) Navigate(_ context.Context, url string) (string, string, error) {
	f.lastOp, f.lastTarget = "navigate", url
	return "Example", url, nil
}
func (f *fakeBrowserBackend) Snapshot(context.Context, int) (string, error) {
	return "Page: Example\nURL: http://x\n\nInteractive elements (use the [n] ref with click/type):\n[1] <a> Home -> /", nil
}
func (f *fakeBrowserBackend) Click(_ context.Context, target string) error {
	f.lastOp, f.lastTarget = "click", target
	return nil
}
func (f *fakeBrowserBackend) Type(_ context.Context, target, text string, submit bool) error {
	f.lastOp, f.lastTarget, f.lastText, f.lastSubmit = "type", target, text, submit
	return nil
}
func (f *fakeBrowserBackend) Eval(_ context.Context, js string) (string, error) {
	f.lastOp, f.lastJS = "eval", js
	if f.evalOut != "" {
		return f.evalOut, nil
	}
	return "42", nil
}
func (f *fakeBrowserBackend) Identity(context.Context) (string, string, error) {
	if f.identityErr != nil {
		err := f.identityErr
		f.identityErr = nil
		return "", "", err
	}
	if len(f.urls) == 0 {
		return "Example", "http://x", nil
	}
	u := f.urls[0]
	if len(f.urls) > 1 {
		f.urls = f.urls[1:]
	}
	return "Example", u, nil
}
func (f *fakeBrowserBackend) Visible() bool { return f.visible }
func (f *fakeBrowserBackend) Press(_ context.Context, chord string) error {
	f.lastOp, f.lastText = "press", chord
	return nil
}
func (f *fakeBrowserBackend) Hover(_ context.Context, target string) error {
	f.lastOp, f.lastTarget = "hover", target
	return nil
}
func (f *fakeBrowserBackend) Select(_ context.Context, target, option string) error {
	f.lastOp, f.lastTarget, f.lastText = "select", target, option
	return nil
}
func (f *fakeBrowserBackend) Upload(_ context.Context, target string, paths []string) error {
	f.lastOp, f.lastTarget, f.files = "upload", target, paths
	return nil
}
func (f *fakeBrowserBackend) HTML(_ context.Context, target string, maxBytes int) (string, error) {
	f.lastOp, f.lastTarget = "html", target
	return "<div id=\"app\">hi</div>", nil
}
func (f *fakeBrowserBackend) PDF(_ context.Context, path string) error {
	f.lastOp, f.lastTarget = "pdf", path
	return nil
}
func (f *fakeBrowserBackend) Resize(_ context.Context, w, h int, mobile bool) error {
	f.lastOp, f.dims, f.mobile = "resize", [2]int{w, h}, mobile
	return nil
}
func (f *fakeBrowserBackend) ScreenshotFull(_ context.Context, path string) error {
	f.lastOp, f.lastTarget = "screenshot-full", path
	return nil
}
func (f *fakeBrowserBackend) Tabs(context.Context) ([]browser.TabInfo, error) {
	f.lastOp = "tabs"
	return f.tabs, nil
}
func (f *fakeBrowserBackend) SwitchTab(_ context.Context, which string) (browser.TabInfo, error) {
	f.lastOp, f.lastTarget = "tab", which
	for _, t := range f.tabs {
		if t.ID == which {
			return t, nil
		}
	}
	if len(f.tabs) > 0 {
		return f.tabs[len(f.tabs)-1], nil
	}
	return browser.TabInfo{}, errors.New("no tabs")
}
func (f *fakeBrowserBackend) Cookies(_ context.Context, domain string) ([]browser.CookieInfo, error) {
	f.lastOp, f.lastText = "cookies", domain
	return f.cookies, nil
}
func (f *fakeBrowserBackend) ClearCookies(context.Context) error {
	f.lastOp = "cookies-clear"
	return nil
}
func (f *fakeBrowserBackend) Screenshot(_ context.Context, path string) error {
	f.lastOp, f.lastTarget = "screenshot", path
	return nil
}
func (f *fakeBrowserBackend) Scroll(_ context.Context, dir, target string) error {
	f.lastOp, f.lastTarget = "scroll", dir+"|"+target
	return nil
}
func (f *fakeBrowserBackend) Back(context.Context) (string, string, error) {
	f.lastOp = "back"
	return "Prev", "http://prev", nil
}
func (f *fakeBrowserBackend) ConsoleTail(int) []browser.ConsoleEntry { return f.console }
func (f *fakeBrowserBackend) NetworkTail(int) []browser.NetworkEntry { return f.network }

// withFakeBrowser injects a fake backend and records every mode the plugin
// asked for, so visibility requests are observable.
func withFakeBrowser(t *testing.T) *fakeBrowserBackend {
	t.Helper()
	fake := &fakeBrowserBackend{}
	prev := acquireBrowser
	acquireBrowser = func(_ context.Context, mode browser.Mode) (BrowserBackend, error) {
		fake.visible = mode == browser.ModeVisible || (fake.visible && mode == browser.ModeDefault)
		return fake, nil
	}
	t.Cleanup(func() { acquireBrowser = prev })
	return fake
}

// modeRecorder swaps acquireBrowser for one that records the requested modes.
func modeRecorder(t *testing.T) (*fakeBrowserBackend, *[]browser.Mode) {
	t.Helper()
	fake := &fakeBrowserBackend{}
	var modes []browser.Mode
	prev := acquireBrowser
	acquireBrowser = func(_ context.Context, mode browser.Mode) (BrowserBackend, error) {
		modes = append(modes, mode)
		return fake, nil
	}
	t.Cleanup(func() { acquireBrowser = prev })
	return fake, &modes
}

func TestParseBrowserInvocation_Envelope(t *testing.T) {
	inv, err := parseBrowserInvocation([]string{`{"cmd":"type","args":{"target":2,"text":"golang","submit":true}}`})
	if err != nil {
		t.Fatal(err)
	}
	if inv.cmd != "type" || inv.target != "2" || inv.text != "golang" || !inv.submit {
		t.Fatalf("unexpected parse: %+v", inv)
	}
}

func TestParseBrowserInvocation_FlattenedEnvelope(t *testing.T) {
	inv, err := parseBrowserInvocation([]string{`{"cmd":"open","url":"http://localhost:3000"}`})
	if err != nil {
		t.Fatal(err)
	}
	if inv.cmd != "open" || inv.url != "http://localhost:3000" {
		t.Fatalf("flattened envelope must parse, got %+v", inv)
	}
}

func TestParseBrowserInvocation_FlatArgs(t *testing.T) {
	inv, err := parseBrowserInvocation([]string{"type", "3", "hello", "world", "--submit"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.target != "3" || inv.text != "hello world" || !inv.submit {
		t.Fatalf("unexpected parse: %+v", inv)
	}
	inv, err = parseBrowserInvocation([]string{"console", "--tail", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.tail != 5 {
		t.Fatalf("expected tail 5, got %+v", inv)
	}
}

func TestBrowserExecute_OpenNormalizesScheme(t *testing.T) {
	fake := withFakeBrowser(t)
	p := NewBuiltinBrowserPlugin()
	out, err := p.Execute(context.Background(), []string{"open", "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastTarget != "https://example.com" {
		t.Fatalf("scheme not normalized: %q", fake.lastTarget)
	}
	if !strings.Contains(out, "Page: Example") {
		t.Fatalf("open must return a snapshot, got: %s", out)
	}
}

func TestBrowserExecute_EvalAndConsole(t *testing.T) {
	fake := withFakeBrowser(t)
	fake.console = []browser.ConsoleEntry{{Kind: "error", Text: "boom at app.js:3"}}
	p := NewBuiltinBrowserPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"eval","args":{"js":"1+41"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if out != "42" || fake.lastJS != "1+41" {
		t.Fatalf("eval mismatch: out=%q js=%q", out, fake.lastJS)
	}

	out, err = p.Execute(context.Background(), []string{"console"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[error] boom at app.js:3") {
		t.Fatalf("console output missing entry: %s", out)
	}
}

func TestBrowserExecute_UnknownCmdAndMissingArgs(t *testing.T) {
	withFakeBrowser(t)
	p := NewBuiltinBrowserPlugin()
	if _, err := p.Execute(context.Background(), []string{"teleport"}); err == nil {
		t.Fatal("unknown cmd must error")
	}
	if _, err := p.Execute(context.Background(), []string{"click"}); err == nil {
		t.Fatal("click without target must error")
	}
	if _, err := p.Execute(context.Background(), nil); err == nil {
		t.Fatal("empty args must error")
	}
}

func TestBrowserExecute_StatusDoesNotLaunch(t *testing.T) {
	prev := browserStatus
	browserStatus = func(context.Context) (string, error) { return "No browser session running", nil }
	t.Cleanup(func() { browserStatus = prev })

	launched := false
	prevAcq := acquireBrowser
	acquireBrowser = func(context.Context, browser.Mode) (BrowserBackend, error) {
		launched = true
		return &fakeBrowserBackend{}, nil
	}
	t.Cleanup(func() { acquireBrowser = prevAcq })

	p := NewBuiltinBrowserPlugin()
	if _, err := p.Execute(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if launched {
		t.Fatal("status must never launch a browser")
	}
}

func TestBrowserCaps_ReadOnlySplit(t *testing.T) {
	p := NewBuiltinBrowserPlugin()
	readOnly := [][]string{
		{"open", "http://x"}, {"snapshot"}, {"screenshot"}, {"console"},
		{"network"}, {"scroll", "down"}, {"back"}, {"status"}, {"close"},
		{"show"}, {"hide"}, {"wait", "--url", "/dash"}, {"html"}, {"pdf"},
		{"resize", "1280", "800"}, {"tabs"}, {"tab", "2"}, {"cookies"},
		{`{"cmd":"open","args":{"url":"http://x","visible":true}}`},
	}
	for _, args := range readOnly {
		if !p.IsReadOnly(args) {
			t.Fatalf("%v must be read-only", args)
		}
	}
	acting := [][]string{
		{"click", "3"}, {"type", "2", "text"}, {`{"cmd":"eval","args":{"js":"x"}}`},
		{"press", "Enter"}, {"hover", "3"}, {"select", "4", "BR"}, {"upload", "2", "/tmp/x"},
		{"cookies", "--clear"}, {`{"cmd":"cookies","args":{"clear":true}}`},
	}
	for _, args := range acting {
		if p.IsReadOnly(args) {
			t.Fatalf("%v must NOT be read-only (security gate)", args)
		}
	}
	if p.IsConcurrencySafe([]string{"snapshot"}) {
		t.Fatal("browser is a single stateful session — never concurrency-safe")
	}
}

func TestBrowserExecute_ScreenshotScrollBackNetwork(t *testing.T) {
	fake := withFakeBrowser(t)
	fake.network = []browser.NetworkEntry{{Method: "GET", URL: "http://api/y", Status: 404, Type: "Fetch"}}
	p := NewBuiltinBrowserPlugin()

	out, err := p.Execute(context.Background(), []string{"screenshot", "--file", "/tmp/x.png"})
	if err != nil || !strings.Contains(out, "/tmp/x.png") {
		t.Fatalf("screenshot: out=%q err=%v", out, err)
	}
	if fake.lastTarget != "/tmp/x.png" {
		t.Fatalf("path not forwarded: %q", fake.lastTarget)
	}

	out, err = p.Execute(context.Background(), []string{"screenshot"})
	if err != nil || !strings.Contains(out, "screenshot-") {
		t.Fatalf("default screenshot path missing: out=%q err=%v", out, err)
	}

	if _, err := p.Execute(context.Background(), []string{"scroll", "down"}); err != nil {
		t.Fatal(err)
	}
	if fake.lastTarget != "down|" {
		t.Fatalf("scroll args wrong: %q", fake.lastTarget)
	}
	if _, err := p.Execute(context.Background(), []string{"scroll", "--to", "#footer"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fake.lastTarget, "|#footer") {
		t.Fatalf("scroll --to not forwarded: %q", fake.lastTarget)
	}

	out, err = p.Execute(context.Background(), []string{"back"})
	if err != nil || !strings.Contains(out, "http://prev") {
		t.Fatalf("back: out=%q err=%v", out, err)
	}

	out, err = p.Execute(context.Background(), []string{"network", "--tail", "10"})
	if err != nil || !strings.Contains(out, "404 GET http://api/y (Fetch)") {
		t.Fatalf("network render wrong: out=%q err=%v", out, err)
	}
}

func TestBrowserExecute_TypeClickSnapshotFlows(t *testing.T) {
	fake := withFakeBrowser(t)
	p := NewBuiltinBrowserPlugin()

	out, err := p.Execute(context.Background(), []string{"type", "2", "hello"})
	if err != nil || !strings.Contains(out, `Typed "hello" into 2.`) {
		t.Fatalf("type: out=%q err=%v", out, err)
	}
	out, err = p.Execute(context.Background(), []string{`{"cmd":"type","args":{"target":"2","text":"q","submit":true}}`})
	if err != nil || !strings.Contains(out, "Page: Example") {
		t.Fatalf("type --submit must return snapshot: out=%q err=%v", out, err)
	}
	if !fake.lastSubmit {
		t.Fatal("submit flag not forwarded")
	}

	out, err = p.Execute(context.Background(), []string{"click", "#go"})
	if err != nil || !strings.Contains(out, "Page: Example") {
		t.Fatalf("click must return snapshot: out=%q err=%v", out, err)
	}
	out, err = p.Execute(context.Background(), []string{"snapshot"})
	if err != nil || !strings.Contains(out, "Interactive elements") {
		t.Fatalf("snapshot: out=%q err=%v", out, err)
	}

	out, err = p.Execute(context.Background(), []string{"console"})
	if err != nil || out != browserMsgNoConsole {
		t.Fatalf("empty console message wrong: out=%q err=%v", out, err)
	}
	if _, err := p.Execute(context.Background(), []string{"type"}); err == nil {
		t.Fatal("type without target must error")
	}
	if _, err := p.Execute(context.Background(), []string{"eval", "   "}); err == nil {
		t.Fatal("eval without js must error")
	}
	if _, err := p.Execute(context.Background(), []string{"open"}); err == nil {
		t.Fatal("open without url must error")
	}
}

func TestBrowserStatusAndCloseCmds(t *testing.T) {
	p := NewBuiltinBrowserPlugin()
	// Real status probe: no session running in the test process.
	out, err := p.Execute(context.Background(), []string{"status"})
	if err != nil || !strings.Contains(out, "No browser session running") {
		t.Fatalf("status: out=%q err=%v", out, err)
	}
	out, err = p.Execute(context.Background(), []string{"close"})
	if err != nil || out != browserMsgClosed {
		t.Fatalf("close: out=%q err=%v", out, err)
	}
}

func TestBrowserDescribeCallAndMeta(t *testing.T) {
	p := NewBuiltinBrowserPlugin()
	if p.Name() != "@browser" || p.Version() == "" || p.Path() == "" {
		t.Fatal("plugin identity incomplete")
	}
	if !strings.Contains(p.Usage(), "snapshot") || !strings.Contains(p.Schema(), "screenshot") ||
		!strings.Contains(p.Description(), "Chrome") {
		t.Fatal("usage/schema/description incomplete")
	}
	for _, args := range [][]string{
		{"open", "http://x"}, {"snapshot"}, {"click", "1"}, {"type", "1", "x"},
		{"eval", "1"}, {"screenshot"}, {"console"}, {"network"}, {"whatever"},
	} {
		if strings.TrimSpace(p.DescribeCall(args)) == "" {
			t.Fatalf("DescribeCall(%v) empty", args)
		}
	}
}

// TestParseBrowserInvocation_FlattenedEnvelopeArgv reproduces the real agent
// bug: the loop flattens {"cmd":"open","args":{"url":X}} into argv
// ["open","--url",X], which the strict parser mistook for the URL "--url".
func TestParseBrowserInvocation_FlattenedEnvelopeArgv(t *testing.T) {
	url := "file:///tmp/form.html"
	inv, err := parseBrowserInvocation([]string{"open", "--url", url})
	if err != nil {
		t.Fatal(err)
	}
	if inv.cmd != "open" || inv.url != url {
		t.Fatalf("flattened open broke: cmd=%q url=%q (want open/%q)", inv.cmd, inv.url, url)
	}

	inv, _ = parseBrowserInvocation([]string{"click", "--target", "3"})
	if inv.target != "3" {
		t.Fatalf("flattened click target: %q", inv.target)
	}
	inv, _ = parseBrowserInvocation([]string{"type", "--target", "2", "--text", "golang", "--submit"})
	if inv.target != "2" || inv.text != "golang" || !inv.submit {
		t.Fatalf("flattened type: %+v", inv)
	}
	inv, _ = parseBrowserInvocation([]string{"eval", "--js", "document.title"})
	if inv.js != "document.title" {
		t.Fatalf("flattened eval js: %q", inv.js)
	}
	inv, _ = parseBrowserInvocation([]string{"scroll", "--direction", "down"})
	if inv.dir != "down" {
		t.Fatalf("flattened scroll dir: %q", inv.dir)
	}
	inv, _ = parseBrowserInvocation([]string{"screenshot", "--file", "/tmp/s.png"})
	if inv.file != "/tmp/s.png" {
		t.Fatalf("flattened screenshot file: %q", inv.file)
	}
	// Bare positional still works (open example.com).
	inv, _ = parseBrowserInvocation([]string{"open", "example.com"})
	if inv.url != "example.com" {
		t.Fatalf("positional open regressed: %q", inv.url)
	}
}

func TestParseBrowserInvocation_VisibilityForms(t *testing.T) {
	cases := []struct {
		args    []string
		set     bool
		visible bool
		url     string
	}{
		{[]string{`{"cmd":"open","args":{"url":"http://x","visible":true}}`}, true, true, "http://x"},
		{[]string{`{"cmd":"open","args":{"url":"http://x","headless":false}}`}, true, true, "http://x"},
		{[]string{`{"cmd":"open","args":{"url":"http://x","visible":"false"}}`}, true, false, "http://x"},
		{[]string{`{"cmd":"open","args":{"url":"http://x"}}`}, false, false, "http://x"},
		// Flattened envelope: bool rendered as a value.
		{[]string{"open", "--url", "http://x", "--visible", "true"}, true, true, "http://x"},
		{[]string{"open", "--url", "http://x", "--headless", "true"}, true, false, "http://x"},
		// Bare flag before the URL: splitFlatArgs pairs them, the parser
		// must hand the URL back instead of treating it as the bool.
		{[]string{"open", "--visible", "http://x"}, true, true, "http://x"},
		{[]string{"open", "http://x", "--visible"}, true, true, "http://x"},
	}
	for _, c := range cases {
		inv, err := parseBrowserInvocation(c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if inv.visibleSet != c.set || inv.visible != c.visible || inv.url != c.url {
			t.Fatalf("%v: set=%t visible=%t url=%q", c.args, inv.visibleSet, inv.visible, inv.url)
		}
	}
	if m := (browserInvocation{cmd: "show"}).mode(); m != browser.ModeVisible {
		t.Fatalf("show must request visible, got %v", m)
	}
	if m := (browserInvocation{cmd: "hide"}).mode(); m != browser.ModeHeadless {
		t.Fatalf("hide must request headless, got %v", m)
	}
	if m := (browserInvocation{cmd: "open"}).mode(); m != browser.ModeDefault {
		t.Fatalf("plain open must keep the running mode, got %v", m)
	}
}

func TestBrowserExecute_ShowHideRequestModes(t *testing.T) {
	fake, modes := modeRecorder(t)
	prevAlive := browserAlive
	browserAlive = func() bool { return true }
	t.Cleanup(func() { browserAlive = prevAlive })
	p := NewBuiltinBrowserPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"show","args":{"url":"https://app.example/login"}}`})
	if err != nil || !strings.Contains(out, "VISIBLE") || !strings.Contains(out, "Page: Example") {
		t.Fatalf("show: out=%q err=%v", out, err)
	}
	if fake.lastOp != "navigate" || fake.lastTarget != "https://app.example/login" {
		t.Fatalf("show with url must navigate, got %s %s", fake.lastOp, fake.lastTarget)
	}
	out, err = p.Execute(context.Background(), []string{"show"})
	if err != nil || out != browserMsgShown {
		t.Fatalf("bare show: out=%q err=%v", out, err)
	}
	out, err = p.Execute(context.Background(), []string{"hide"})
	if err != nil || out != browserMsgHidden {
		t.Fatalf("hide: out=%q err=%v", out, err)
	}
	if _, err := p.Execute(context.Background(), []string{"open", "--visible", "http://x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Execute(context.Background(), []string{"snapshot"}); err != nil {
		t.Fatal(err)
	}
	want := []browser.Mode{browser.ModeVisible, browser.ModeVisible, browser.ModeHeadless, browser.ModeVisible, browser.ModeDefault}
	if len(*modes) != len(want) {
		t.Fatalf("modes = %v, want %v", *modes, want)
	}
	for i := range want {
		if (*modes)[i] != want[i] {
			t.Fatalf("modes = %v, want %v", *modes, want)
		}
	}
}

func TestBrowserExecute_WaitConditions(t *testing.T) {
	fake := withFakeBrowser(t)
	p := NewBuiltinBrowserPlugin()

	// URL condition met on the second poll.
	fake.urls = []string{"https://app/login", "https://app/dashboard"}
	out, err := p.Execute(context.Background(), []string{`{"cmd":"wait","args":{"url":"/dashboard","timeout":5}}`})
	if err != nil || !strings.Contains(out, "Condition met") || !strings.Contains(out, "https://app/dashboard") {
		t.Fatalf("wait url: out=%q err=%v", out, err)
	}

	// Text condition goes through the snapshot; the fake snapshot has "Home".
	out, err = p.Execute(context.Background(), []string{"wait", "--text", "home", "--timeout", "5"})
	if err != nil || !strings.Contains(out, "Condition met") {
		t.Fatalf("wait text: out=%q err=%v", out, err)
	}

	// Selector condition through Eval.
	fake.evalOut = "true"
	out, err = p.Execute(context.Background(), []string{`{"cmd":"wait","args":{"selector":"#ready","timeout":5}}`})
	if err != nil || !strings.Contains(out, "Condition met") {
		t.Fatalf("wait selector: out=%q err=%v js=%q", out, err, fake.lastJS)
	}
	if !strings.Contains(fake.lastJS, "#ready") {
		t.Fatalf("selector must reach Eval: %q", fake.lastJS)
	}
	fake.evalOut = ""

	// No condition is an error that points at @ask.
	if _, err := p.Execute(context.Background(), []string{"wait"}); err == nil || !strings.Contains(err.Error(), "@ask") {
		t.Fatalf("wait without condition must error and mention @ask, got %v", err)
	}

	// Timeout is a result (the model decides), not an error.
	fake.urls = []string{"https://app/login"}
	out, err = p.Execute(context.Background(), []string{`{"cmd":"wait","args":{"url":"/never","timeout":1}}`})
	if err != nil || !strings.Contains(out, "Timed out") || !strings.Contains(out, "https://app/login") {
		t.Fatalf("wait timeout: out=%q err=%v", out, err)
	}
	if browserWaitBound(0) != browserWaitDefault || browserWaitBound(100000) != browserWaitMax || browserWaitBound(30) != 30*time.Second {
		t.Fatal("wait bound clamp broken")
	}
}

func TestBrowserExecute_ExtendedVerbs(t *testing.T) {
	fake := withFakeBrowser(t)
	p := NewBuiltinBrowserPlugin()
	run := func(args ...string) string {
		t.Helper()
		out, err := p.Execute(context.Background(), args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}

	if out := run(`{"cmd":"press","args":{"key":"Control+a"}}`); fake.lastOp != "press" || fake.lastText != "Control+a" || !strings.Contains(out, "Pressed Control+a") {
		t.Fatalf("press: %q op=%s", out, fake.lastOp)
	}
	run("press", "Enter")
	if fake.lastText != "Enter" {
		t.Fatalf("flat press: %q", fake.lastText)
	}
	run("hover", "5")
	if fake.lastOp != "hover" || fake.lastTarget != "5" {
		t.Fatalf("hover: %s %s", fake.lastOp, fake.lastTarget)
	}
	run(`{"cmd":"select","args":{"target":"4","value":"BR"}}`)
	if fake.lastOp != "select" || fake.lastTarget != "4" || fake.lastText != "BR" {
		t.Fatalf("select: %s %s %s", fake.lastOp, fake.lastTarget, fake.lastText)
	}
	run("select", "4", "Brazil", "South")
	if fake.lastText != "Brazil South" {
		t.Fatalf("flat select joins text: %q", fake.lastText)
	}
	run(`{"cmd":"upload","args":{"target":"2","files":["/tmp/a.png","/tmp/b.png"]}}`)
	if fake.lastOp != "upload" || len(fake.files) != 2 {
		t.Fatalf("upload: %s %v", fake.lastOp, fake.files)
	}
	run("upload", "--target", "2", "--file", "/tmp/x.pdf")
	if len(fake.files) != 1 || fake.files[0] != "/tmp/x.pdf" {
		t.Fatalf("flat upload: %v", fake.files)
	}
	if out := run("html", "#app"); fake.lastOp != "html" || fake.lastTarget != "#app" || !strings.Contains(out, "<div") {
		t.Fatalf("html: %q", out)
	}
	if out := run(`{"cmd":"pdf","args":{"file":"/tmp/p.pdf"}}`); fake.lastTarget != "/tmp/p.pdf" || !strings.Contains(out, "PDF saved") {
		t.Fatalf("pdf: %q", out)
	}
	if out := run("pdf"); !strings.HasSuffix(fake.lastTarget, ".pdf") || !strings.Contains(out, "PDF saved") {
		t.Fatalf("pdf default path: %q %q", out, fake.lastTarget)
	}
	run(`{"cmd":"resize","args":{"width":390,"height":844,"mobile":true}}`)
	if fake.dims != [2]int{390, 844} || !fake.mobile {
		t.Fatalf("resize: %v %t", fake.dims, fake.mobile)
	}
	run("resize", "1280x800")
	if fake.dims != [2]int{1280, 800} || fake.mobile {
		t.Fatalf("resize WxH: %v %t", fake.dims, fake.mobile)
	}
	run("resize", "375", "667", "mobile")
	if fake.dims != [2]int{375, 667} || !fake.mobile {
		t.Fatalf("resize positional mobile: %v %t", fake.dims, fake.mobile)
	}
	if _, err := p.Execute(context.Background(), []string{"resize"}); err == nil {
		t.Fatal("resize without dims must error")
	}
	run(`{"cmd":"screenshot","args":{"full":true,"file":"/tmp/full.png"}}`)
	if fake.lastOp != "screenshot-full" || fake.lastTarget != "/tmp/full.png" {
		t.Fatalf("screenshot full: %s %s", fake.lastOp, fake.lastTarget)
	}
	run("screenshot", "--full")
	if fake.lastOp != "screenshot-full" {
		t.Fatalf("flat screenshot --full: %s", fake.lastOp)
	}

	fake.tabs = []browser.TabInfo{{ID: "t1", Title: "App", URL: "https://app/", Current: true}, {ID: "t2", Title: "Sign in with X", URL: "https://x/oauth"}}
	if out := run("tabs"); !strings.Contains(out, "*[1] App") || !strings.Contains(out, "[2] Sign in with X") {
		t.Fatalf("tabs: %q", out)
	}
	if out := run("tab", "2"); fake.lastOp != "tab" || fake.lastTarget != "2" || !strings.Contains(out, "Now driving tab") {
		t.Fatalf("tab: %q", out)
	}
	fake.tabs = nil
	if out := run("tabs"); out != browserMsgNoTabs {
		t.Fatalf("no tabs: %q", out)
	}

	fake.cookies = []browser.CookieInfo{{Name: "sid", Domain: ".app.example", Path: "/", HTTPOnly: true, Secure: true}}
	out := run(`{"cmd":"cookies","args":{"domain":"app.example"}}`)
	if fake.lastOp != "cookies" || fake.lastText != "app.example" || !strings.Contains(out, "sid") || !strings.Contains(out, "httpOnly,secure") || !strings.Contains(out, "values withheld") {
		t.Fatalf("cookies: %q", out)
	}
	if out := run("cookies", "--clear"); fake.lastOp != "cookies-clear" || out != browserMsgCookiesCleared {
		t.Fatalf("cookies clear: %q", out)
	}
	fake.cookies = nil
	if out := run("cookies"); out != browserMsgNoCookies {
		t.Fatalf("no cookies: %q", out)
	}

	// Missing-argument errors are instructive, never panics.
	for _, args := range [][]string{{"press"}, {"hover"}, {"select", "4"}, {"upload", "2"}, {"tab"}} {
		if _, err := p.Execute(context.Background(), args); err == nil {
			t.Fatalf("%v must error", args)
		}
	}
}

// TestBrowserExecute_ExtUnsupportedBackend wraps the fake so only
// BrowserBackend is visible: the second-tier verbs must fail with a clear
// message, not a panic.
func TestBrowserExecute_ExtUnsupportedBackend(t *testing.T) {
	prev := acquireBrowser
	acquireBrowser = func(context.Context, browser.Mode) (BrowserBackend, error) {
		return struct{ BrowserBackend }{&fakeBrowserBackend{}}, nil
	}
	t.Cleanup(func() { acquireBrowser = prev })
	p := NewBuiltinBrowserPlugin()
	for _, args := range [][]string{{"press", "Enter"}, {"tabs"}, {"screenshot", "--full"}} {
		_, err := p.Execute(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "does not support") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestBrowserDescribeCall_NewVerbs(t *testing.T) {
	p := NewBuiltinBrowserPlugin()
	for _, args := range [][]string{
		{"show"}, {"hide"}, {"wait", "--url", "/x"}, {"press", "Enter"}, {"hover", "1"},
		{"select", "1", "a"}, {"upload", "1", "/tmp/f"}, {"html"}, {"pdf"}, {"resize", "1", "1"},
		{"tabs"}, {"tab", "1"}, {"cookies"}, {"cookies", "--clear"},
		{`{"cmd":"open","args":{"url":"http://x","visible":true}}`},
	} {
		if strings.TrimSpace(p.DescribeCall(args)) == "" {
			t.Fatalf("DescribeCall(%v) empty", args)
		}
	}
	if !strings.Contains(p.Usage(), "show") || !strings.Contains(p.Schema(), "\"wait\"") || !strings.Contains(p.Description(), "visible") {
		t.Fatal("usage/schema/description must advertise the visible hand-off")
	}
}

func TestBrowserExecute_HideWithoutSessionNeverLaunches(t *testing.T) {
	_, modes := modeRecorder(t)
	prevAlive := browserAlive
	browserAlive = func() bool { return false }
	t.Cleanup(func() { browserAlive = prevAlive })
	p := NewBuiltinBrowserPlugin()
	out, err := p.Execute(context.Background(), []string{"hide"})
	if err != nil || out != browserMsgNotRunning {
		t.Fatalf("hide without session: out=%q err=%v", out, err)
	}
	if len(*modes) != 0 {
		t.Fatalf("hide without a session must not acquire a browser, got %v", *modes)
	}
}

func TestBrowserExecute_WaitReportsClosedPageAsResult(t *testing.T) {
	fake := withFakeBrowser(t)
	p := NewBuiltinBrowserPlugin()
	fake.identityErr = browser.ErrPageClosed
	out, err := p.Execute(context.Background(), []string{`{"cmd":"wait","args":{"url":"/dashboard","timeout":5}}`})
	if err != nil || out != browserMsgPageClosed {
		t.Fatalf("closed page during wait must be a result the model can act on: out=%q err=%v", out, err)
	}
	// Any other backend error is still an error.
	fake.identityErr = errors.New("boom")
	if _, err := p.Execute(context.Background(), []string{"wait", "--url", "/x", "--timeout", "5"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("generic error must propagate, got %v", err)
	}
}

func TestBrowserNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"example.com":           "https://example.com",
		"http://x":              "http://x",
		"about:blank":           "about:blank",
		"data:text/html,<b>x":   "data:text/html,<b>x",
		"file:///tmp/form.html": "file:///tmp/form.html",
		" localhost:3000 ":      "https://localhost:3000",
	}
	for in, want := range cases {
		if got := browserNormalizeURL(in); got != want {
			t.Fatalf("browserNormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
	fake := withFakeBrowser(t)
	p := NewBuiltinBrowserPlugin()
	if _, err := p.Execute(context.Background(), []string{"open", "about:blank"}); err != nil {
		t.Fatal(err)
	}
	if fake.lastTarget != "about:blank" {
		t.Fatalf("about:blank must reach the backend untouched, got %q", fake.lastTarget)
	}
}
