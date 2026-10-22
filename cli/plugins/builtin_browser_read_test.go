/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_browser_read_test.go — the reading verbs, the richer click/type
 * forms, wait --unblocked/--idle, network --body, the screenshot
 * attachment and the throwaway-profile notes of @browser, against a fake
 * that implements BrowserReadBackend.
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

// fakeReadBrowser is fakeBrowserBackend plus the BrowserReadBackend surface.
type fakeReadBrowser struct {
	fakeBrowserBackend
	clickOpts  browser.ClickOptions
	clickNote  string
	typedAppnd bool
	password   bool
	challenge  []string // Challenge answers, popped per call (last repeats)
	quiet      bool
	body       string
	bodyErr    error
	downloads  []browser.DownloadInfo
	offset     int
}

func (f *fakeReadBrowser) SnapshotAt(_ context.Context, _ int, offset int) (string, error) {
	f.lastOp, f.offset = "snapshot-at", offset
	return "Page: Example\nURL: http://x\n\nPage text:\ncontinued", nil
}
func (f *fakeReadBrowser) Text(_ context.Context, target string, offset, _ int) (string, error) {
	f.lastOp, f.lastTarget, f.offset = "text", target, offset
	return "plain text [text bytes 0–10 of 10; end of page]", nil
}
func (f *fakeReadBrowser) Find(_ context.Context, query string, _ int) (string, error) {
	f.lastOp, f.lastText = "find", query
	return "Page: Example\nURL: http://x\n\nInteractive elements matching \"" + query + "\":\n[3] <a> " + query, nil
}
func (f *fakeReadBrowser) ClickWith(_ context.Context, target string, opts browser.ClickOptions) (string, error) {
	f.lastOp, f.lastTarget, f.clickOpts = "click-with", target, opts
	return f.clickNote, nil
}
func (f *fakeReadBrowser) TypeWith(_ context.Context, target, text string, append, submit bool) (bool, error) {
	f.lastOp, f.lastTarget, f.lastText, f.typedAppnd, f.lastSubmit = "type-with", target, text, append, submit
	return f.password, nil
}
func (f *fakeReadBrowser) Clear(_ context.Context, target string) error {
	f.lastOp, f.lastTarget = "clear", target
	return nil
}
func (f *fakeReadBrowser) Forward(context.Context) (string, string, error) {
	f.lastOp = "forward"
	return "Next", "http://next", nil
}
func (f *fakeReadBrowser) Reload(context.Context) (string, string, error) {
	f.lastOp = "reload"
	return "Example", "http://x", nil
}
func (f *fakeReadBrowser) Challenge(context.Context) (string, error) {
	if len(f.challenge) == 0 {
		return "", nil
	}
	v := f.challenge[0]
	if len(f.challenge) > 1 {
		f.challenge = f.challenge[1:]
	}
	return v, nil
}
func (f *fakeReadBrowser) NetworkQuiet(time.Duration) bool { return f.quiet }
func (f *fakeReadBrowser) ResponseBody(_ context.Context, filter string, _ int) (browser.NetworkEntry, string, error) {
	f.lastOp, f.lastText = "body", filter
	if f.bodyErr != nil {
		return browser.NetworkEntry{}, "", f.bodyErr
	}
	return browser.NetworkEntry{Method: "GET", URL: "http://x/api/items", Status: 200}, f.body, nil
}
func (f *fakeReadBrowser) Downloads() []browser.DownloadInfo { return f.downloads }

func withFakeReadBrowser(t *testing.T) *fakeReadBrowser {
	t.Helper()
	stubPublicLookup(t)
	fake := &fakeReadBrowser{}
	prev := acquireBrowser
	acquireBrowser = func(context.Context, browser.Mode) (BrowserBackend, error) { return fake, nil }
	t.Cleanup(func() { acquireBrowser = prev })
	return fake
}

func TestParseBrowserInvocation_ReadFlags(t *testing.T) {
	cases := []struct {
		args []string
		want func(browserInvocation) bool
	}{
		{[]string{`{"cmd":"snapshot","args":{"offset":12000,"max":4000}}`}, func(i browserInvocation) bool { return i.cmd == "snapshot" && i.offset == 12000 && i.max == 4000 }},
		{[]string{"snapshot", "--offset", "500"}, func(i browserInvocation) bool { return i.offset == 500 }},
		{[]string{`{"cmd":"text","args":{"target":"article","offset":10}}`}, func(i browserInvocation) bool { return i.cmd == "text" && i.target == "article" && i.offset == 10 }},
		{[]string{"text", "#main", "--offset", "7"}, func(i browserInvocation) bool { return i.target == "#main" && i.offset == 7 }},
		{[]string{`{"cmd":"find","args":{"query":"Sign in"}}`}, func(i browserInvocation) bool { return i.cmd == "find" && i.text == "Sign in" }},
		{[]string{"find", "Sign", "in"}, func(i browserInvocation) bool { return i.text == "Sign in" }},
		{[]string{`{"cmd":"click","args":{"target":5,"count":2,"button":"Right"}}`}, func(i browserInvocation) bool { return i.target == "5" && i.count == 2 && i.button == "right" }},
		{[]string{"click", "5", "--count", "3"}, func(i browserInvocation) bool { return i.count == 3 }},
		{[]string{`{"cmd":"type","args":{"target":"2","text":" more","append":true}}`}, func(i browserInvocation) bool { return i.appendTo && i.text == " more" }},
		{[]string{"type", "2", "--append", "--text", "x"}, func(i browserInvocation) bool { return i.appendTo && i.text == "x" }},
		{[]string{"clear", "2"}, func(i browserInvocation) bool { return i.cmd == "clear" && i.target == "2" }},
		{[]string{`{"cmd":"wait","args":{"unblocked":true,"idle":true,"timeout":30}}`}, func(i browserInvocation) bool { return i.unblocked && i.idle && i.timeout == 30 }},
		{[]string{"wait", "--unblocked", "--idle"}, func(i browserInvocation) bool { return i.unblocked && i.idle }},
		{[]string{`{"cmd":"network","args":{"filter":"/api/","body":true,"tail":5}}`}, func(i browserInvocation) bool { return i.filter == "/api/" && i.body && i.tail == 5 }},
		{[]string{"network", "--filter", "/api/", "--body"}, func(i browserInvocation) bool { return i.filter == "/api/" && i.body }},
		{[]string{`{"cmd":"screenshot","args":{"view":false}}`}, func(i browserInvocation) bool { return i.noView }},
		{[]string{`{"cmd":"screenshot","args":{"view":true}}`}, func(i browserInvocation) bool { return !i.noView }},
		{[]string{"screenshot", "--view", "false"}, func(i browserInvocation) bool { return i.noView }},
		{[]string{"screenshot"}, func(i browserInvocation) bool { return !i.noView }},
	}
	for _, c := range cases {
		inv, err := parseBrowserInvocation(c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if !c.want(inv) {
			t.Fatalf("%v parsed as %+v", c.args, inv)
		}
	}
}

func TestBrowserExecute_ReadVerbs(t *testing.T) {
	fake := withFakeReadBrowser(t)
	p := NewBuiltinBrowserPlugin()
	ctx := context.Background()

	out, err := p.Execute(ctx, []string{`{"cmd":"snapshot","args":{"offset":12000}}`})
	if err != nil || fake.lastOp != "snapshot-at" || fake.offset != 12000 || !strings.Contains(out, "continued") {
		t.Fatalf("snapshot offset: %q %v (%s)", out, err, fake.lastOp)
	}
	if _, err := p.Execute(ctx, []string{"snapshot"}); err != nil || fake.lastOp != "snapshot-at" && fake.lastOp != "snapshot" {
		t.Fatalf("plain snapshot: %v", err)
	}
	out, err = p.Execute(ctx, []string{"text", "article", "--offset", "3"})
	if err != nil || fake.lastOp != "text" || fake.lastTarget != "article" || fake.offset != 3 || !strings.HasPrefix(out, "plain text") {
		t.Fatalf("text: %q %v", out, err)
	}
	out, err = p.Execute(ctx, []string{`{"cmd":"find","args":{"text":"Sign in"}}`})
	if err != nil || fake.lastOp != "find" || fake.lastText != "Sign in" || !strings.Contains(out, "[3] <a> Sign in") {
		t.Fatalf("find: %q %v", out, err)
	}
	if _, err := p.Execute(ctx, []string{"find"}); err == nil || !strings.Contains(err.Error(), "missing text") {
		t.Fatalf("find without text: %v", err)
	}
	out, err = p.Execute(ctx, []string{"clear", "2"})
	if err != nil || fake.lastOp != "clear" || fake.lastTarget != "2" || out != "Cleared 2." {
		t.Fatalf("clear: %q %v", out, err)
	}
	if _, err := p.Execute(ctx, []string{"clear"}); err == nil {
		t.Fatal("clear without target must error")
	}
	out, err = p.Execute(ctx, []string{"forward"})
	if err != nil || fake.lastOp != "forward" || !strings.Contains(out, "Went forward to: Next") {
		t.Fatalf("forward: %q %v", out, err)
	}
	out, err = p.Execute(ctx, []string{"reload"})
	if err != nil || !strings.Contains(out, "Reloaded: Example") || !strings.Contains(out, "Page: Example") {
		t.Fatalf("reload must show the page again: %q %v", out, err)
	}
	out, err = p.Execute(ctx, []string{"downloads"})
	if err != nil || out != browserMsgNoDownloads {
		t.Fatalf("downloads empty: %q %v", out, err)
	}
	fake.downloads = []browser.DownloadInfo{{URL: "http://x/r.pdf", File: "/dl/r.pdf", State: "completed", Received: 10, Total: 10}, {URL: "http://x/big.zip", State: "inProgress", Received: 5, Total: 100}}
	out, err = p.Execute(ctx, []string{"downloads"})
	if err != nil || !strings.Contains(out, "2 download(s)") || !strings.Contains(out, "completed  /dl/r.pdf 10/10 bytes  from http://x/r.pdf") || !strings.Contains(out, "(path not known yet)") {
		t.Fatalf("downloads: %q %v", out, err)
	}
}

func TestBrowserExecute_ClickTypeDetail(t *testing.T) {
	fake := withFakeReadBrowser(t)
	p := NewBuiltinBrowserPlugin()
	ctx := context.Background()

	out, err := p.Execute(ctx, []string{`{"cmd":"click","args":{"target":"5","count":2,"button":"right"}}`})
	if err != nil || fake.lastOp != "click-with" || fake.clickOpts.Count != 2 || fake.clickOpts.Button != "right" || !strings.Contains(out, "Page: Example") {
		t.Fatalf("click detail: %q %v %+v", out, err, fake.clickOpts)
	}
	fake.clickNote = "clicked 5 programmatically — its center is covered by <div.overlay>"
	out, err = p.Execute(ctx, []string{"click", "5"})
	if err != nil || !strings.HasPrefix(out, "Note: clicked 5 programmatically") || !strings.Contains(out, "Page: Example") {
		t.Fatalf("click note: %q %v", out, err)
	}

	out, err = p.Execute(ctx, []string{`{"cmd":"type","args":{"target":"2","text":" more","append":true}}`})
	if err != nil || fake.lastOp != "type-with" || !fake.typedAppnd || out != `Typed " more" into 2.` {
		t.Fatalf("type append: %q %v", out, err)
	}
	fake.password = true
	out, err = p.Execute(ctx, []string{"type", "1", "hunter2"})
	if err != nil || strings.Contains(out, "hunter2") || !strings.Contains(out, "password field") {
		t.Fatalf("password must not be echoed: %q %v", out, err)
	}
	fake.password = false
	out, err = p.Execute(ctx, []string{`{"cmd":"type","args":{"target":"2","text":"go","submit":true}}`})
	if err != nil || !fake.lastSubmit || !strings.Contains(out, "Page: Example") {
		t.Fatalf("type submit shows the page: %q %v", out, err)
	}

	// A first-tier backend gets a clear refusal for count/button/append.
	basic := withFakeBrowser(t)
	if _, err := p.Execute(ctx, []string{"click", "5", "--count", "2"}); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("count on a basic backend: %v", err)
	}
	if _, err := p.Execute(ctx, []string{"type", "2", "x", "--append"}); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("append on a basic backend: %v", err)
	}
	if _, err := p.Execute(ctx, []string{"click", "5"}); err != nil || basic.lastOp != "click" {
		t.Fatalf("plain click on a basic backend: %v %s", err, basic.lastOp)
	}
	if _, err := p.Execute(ctx, []string{"text"}); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("text on a basic backend: %v", err)
	}
}

func TestBrowserExecute_NetworkFilterAndBody(t *testing.T) {
	fake := withFakeReadBrowser(t)
	fake.network = []browser.NetworkEntry{
		{Method: "GET", URL: "http://x/app.js", Status: 200, Type: "Script"},
		{Method: "POST", URL: "http://x/api/items", Status: 201, Type: "Fetch"},
		{Method: "GET", URL: "http://x/api/me", Status: 401, Type: "Fetch"},
	}
	fake.body = `{"items":[1]}`
	p := NewBuiltinBrowserPlugin()
	ctx := context.Background()

	out, err := p.Execute(ctx, []string{`{"cmd":"network","args":{"filter":"/api/"}}`})
	if err != nil || strings.Contains(out, "app.js") || !strings.Contains(out, "Last 2 network response(s)") {
		t.Fatalf("filter: %q %v", out, err)
	}
	out, err = p.Execute(ctx, []string{"network", "--filter", "/api/", "--tail", "1"})
	if err != nil || strings.Contains(out, "/api/items") || !strings.Contains(out, "401 GET http://x/api/me") {
		t.Fatalf("filter+tail: %q %v", out, err)
	}
	out, err = p.Execute(ctx, []string{`{"cmd":"network","args":{"filter":"items","body":true}}`})
	if err != nil || fake.lastOp != "body" || fake.lastText != "items" || !strings.Contains(out, "Body of 200 GET http://x/api/items:\n{\"items\":[1]}") {
		t.Fatalf("body: %q %v", out, err)
	}
	fake.bodyErr = errors.New("no longer available")
	if _, err := p.Execute(ctx, []string{"network", "--body"}); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("body error: %v", err)
	}
	if _, err := p.Execute(ctx, []string{"network", "--filter", "zzz"}); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserExecute_WaitUnblockedAndIdle(t *testing.T) {
	fake := withFakeReadBrowser(t)
	fake.challenge = []string{"Cloudflare", "Cloudflare", ""}
	fake.quiet = true
	p := NewBuiltinBrowserPlugin()
	ctx := context.Background()

	out, err := p.Execute(ctx, []string{`{"cmd":"wait","args":{"unblocked":true,"timeout":10}}`})
	if err != nil || !strings.HasPrefix(out, "Condition met after") {
		t.Fatalf("unblocked: %q %v", out, err)
	}
	fake.challenge = []string{"Cloudflare"}
	out, err = p.Execute(ctx, []string{"wait", "--unblocked", "--timeout", "1"})
	if err != nil || !strings.Contains(out, "Timed out") || !strings.Contains(out, "the bot challenge to clear") {
		t.Fatalf("unblocked timeout: %q %v", out, err)
	}
	fake.challenge = nil
	fake.quiet = false
	out, err = p.Execute(ctx, []string{"wait", "--idle", "--timeout", "1"})
	if err != nil || !strings.Contains(out, "the network to go quiet") {
		t.Fatalf("idle timeout: %q %v", out, err)
	}
	fake.quiet = true
	if out, err = p.Execute(ctx, []string{"wait", "--idle"}); err != nil || !strings.HasPrefix(out, "Condition met") {
		t.Fatalf("idle: %q %v", out, err)
	}
	if p.DescribeCall([]string{"wait", "--unblocked", "--idle"}) == "" {
		t.Fatal("describe wait")
	}
}

func TestBrowserExecute_ScreenshotAttachesToTheConversation(t *testing.T) {
	withFakeReadBrowser(t)
	t.Setenv("CHATCLI_AGENT_TMPDIR", t.TempDir())
	p := NewBuiltinBrowserPlugin()
	ctx := context.Background()

	// Without a vision adapter the result is just the path.
	SetViewAdapter(nil)
	out, err := p.Execute(ctx, []string{"screenshot"})
	if err != nil || !strings.HasPrefix(out, "Screenshot saved to ") || strings.Contains(out, "staged") {
		t.Fatalf("no adapter: %q %v", out, err)
	}

	view := &fakeViewAdapter{}
	SetViewAdapter(view)
	t.Cleanup(func() { SetViewAdapter(nil) })
	out, err = p.Execute(ctx, []string{"screenshot", "--full"})
	if err != nil || view.lastPath == "" || !strings.Contains(out, "Screenshot saved to "+view.lastPath) || !strings.Contains(out, "staged") {
		t.Fatalf("attached: %q %v (view %q)", out, err, view.lastPath)
	}
	view.lastPath = ""
	out, err = p.Execute(ctx, []string{`{"cmd":"screenshot","args":{"view":false}}`})
	if err != nil || view.lastPath != "" || strings.Contains(out, "staged") {
		t.Fatalf("view:false must only save: %q %v", out, err)
	}
	failing := &fakeViewAdapter{err: errors.New("no agent loop is running")}
	SetViewAdapter(failing)
	out, err = p.Execute(ctx, []string{"screenshot"})
	if err != nil || !strings.Contains(out, "not attached to the conversation: no agent loop is running") || !strings.Contains(out, "@view") {
		t.Fatalf("attachment failure is a note, not an error: %q %v", out, err)
	}
}

func TestBrowserCloseAndStatusMentionThrowawayProfile(t *testing.T) {
	prevEph, prevStatus := browserEphemeral, browserStatus
	t.Cleanup(func() { browserEphemeral, browserStatus = prevEph, prevStatus })
	browserEphemeral = func() bool { return true }
	p := NewBuiltinBrowserPlugin()
	out, err := p.Execute(context.Background(), []string{"close"})
	if err != nil || out != browserMsgClosedLogins {
		t.Fatalf("close on a throwaway profile: %q %v", out, err)
	}
	browserEphemeral = func() bool { return false }
	if out, _ := p.Execute(context.Background(), []string{"close"}); out != browserMsgClosed {
		t.Fatalf("close on a persistent profile: %q", out)
	}
}

func TestBrowserCaps_ReadVerbsSplitAndDescribe(t *testing.T) {
	p := NewBuiltinBrowserPlugin()
	for _, args := range [][]string{{"text"}, {"find", "x"}, {"downloads"}, {"forward"}, {"reload"}, {"network", "--body"}, {`{"cmd":"snapshot","args":{"offset":5}}`}} {
		if !p.IsReadOnly(args) {
			t.Fatalf("%v must be read-only", args)
		}
	}
	for _, args := range [][]string{{"clear", "2"}, {`{"cmd":"click","args":{"target":"1","count":2}}`}, {`{"cmd":"type","args":{"target":"1","text":"x","append":true}}`}} {
		if p.IsReadOnly(args) {
			t.Fatalf("%v must NOT be read-only", args)
		}
	}
	for _, args := range [][]string{{"text"}, {"find", "x"}, {"clear", "2"}, {"forward"}, {"reload"}, {"downloads"}, {"network", "--body"}} {
		d := p.DescribeCall(args)
		if d == "" || d == "Driving the browser" {
			t.Fatalf("DescribeCall(%v) = %q", args, d)
		}
	}
	if p.Version() != "1.1.0" {
		t.Fatalf("version = %s", p.Version())
	}
	for _, want := range []string{`"name": "text"`, `"name": "find"`, `"name": "clear"`, `"name": "downloads"`, `"name": "forward"`, `"name": "reload"`, "unblocked", `\"count\":2`, `\"append\":true`, `\"view\":false`, `\"body\":true`} {
		if !strings.Contains(p.Schema(), want) {
			t.Fatalf("schema missing %s", want)
		}
	}
	for _, want := range []string{"text [target]", "find {text}", "clear {target}", "--unblocked", "--idle", "downloads", "forward", "Bot challenge detected", "Password fields never show"} {
		if !strings.Contains(p.Usage(), want) {
			t.Fatalf("usage missing %q", want)
		}
	}
}
