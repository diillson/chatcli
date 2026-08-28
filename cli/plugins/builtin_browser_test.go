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
	"strings"
	"testing"

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
	return "42", nil
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

func withFakeBrowser(t *testing.T) *fakeBrowserBackend {
	t.Helper()
	fake := &fakeBrowserBackend{}
	prev := acquireBrowser
	acquireBrowser = func(context.Context) (BrowserBackend, error) { return fake, nil }
	t.Cleanup(func() { acquireBrowser = prev })
	return fake
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
	acquireBrowser = func(context.Context) (BrowserBackend, error) {
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
	}
	for _, args := range readOnly {
		if !p.IsReadOnly(args) {
			t.Fatalf("%v must be read-only", args)
		}
	}
	acting := [][]string{
		{"click", "3"}, {"type", "2", "text"}, {`{"cmd":"eval","args":{"js":"x"}}`},
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
