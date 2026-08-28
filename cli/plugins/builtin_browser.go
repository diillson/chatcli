/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * @browser — drive a real local Chrome/Chromium over the DevTools protocol:
 * navigate, read the page as text (numbered interactive elements included),
 * click, type, run JavaScript, capture screenshots, and inspect the page's
 * console and network activity. The verification loop for anything web: the
 * agent builds a frontend, then SEES it and debugs it.
 *
 * Zero new dependencies: pkg/browser speaks CDP directly over the websocket
 * client ChatCLI already ships, against a locally installed browser. No
 * driver, no downloaded runtime, no API key.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/pkg/browser"
)

// BrowserBackend is the session surface the plugin drives. Concrete: one
// pkg/browser.Session; swapped for a fake in tests.
type BrowserBackend interface {
	Navigate(ctx context.Context, url string) (title, finalURL string, err error)
	Snapshot(ctx context.Context, maxBytes int) (string, error)
	Click(ctx context.Context, target string) error
	Type(ctx context.Context, target, text string, submit bool) error
	Eval(ctx context.Context, js string) (string, error)
	Screenshot(ctx context.Context, path string) error
	Scroll(ctx context.Context, direction, target string) error
	Back(ctx context.Context) (title, url string, err error)
	ConsoleTail(n int) []browser.ConsoleEntry
	NetworkTail(n int) []browser.NetworkEntry
}

// acquireBrowser returns the live backend, launching the browser on first
// use. Package variable so tests inject a fake without a real Chrome.
var acquireBrowser = func(ctx context.Context) (BrowserBackend, error) {
	return browser.Acquire(ctx)
}

const (
	browserOpenTimeout = 60 * time.Second // may include a cold browser launch
	browserOpTimeout   = 25 * time.Second
	browserDefaultTail = 30
)

// BuiltinBrowserPlugin is the @browser tool.
type BuiltinBrowserPlugin struct{}

// NewBuiltinBrowserPlugin returns a ready-to-register plugin.
func NewBuiltinBrowserPlugin() *BuiltinBrowserPlugin { return &BuiltinBrowserPlugin{} }

// Name returns "@browser".
func (*BuiltinBrowserPlugin) Name() string { return "@browser" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinBrowserPlugin) Description() string {
	return "Drive a real local Chrome/Chromium browser: open a URL, read the rendered page as text with numbered interactive elements, click and type into them, run JavaScript, capture screenshots, and inspect the page's console messages and network responses. Use it to VERIFY web work — open the app you just built or changed, interact with it, and debug it from its console/network activity. Requires a locally installed Chrome, Chromium, Brave or Edge."
}

// Usage explains the canonical invocation forms.
func (*BuiltinBrowserPlugin) Usage() string {
	return `<tool_call name="@browser" args='{"cmd":"open","args":{"url":"http://localhost:3000"}}' />
<tool_call name="@browser" args='{"cmd":"click","args":{"target":"3"}}' />

Subcommands:
  open {url}                       navigate to url (launches the browser on first use), returns a page snapshot
  snapshot [--max N]               current page as text: title, url, numbered interactive elements, visible text
  click {target}                   click an element — target is a [n] ref from the last snapshot or a CSS selector
  type {target} {text} [--submit]  type into an input; --submit presses Enter / submits its form
  scroll [down|up|top|bottom|--to target]   move the viewport
  eval {javascript}                run a JS expression in the page, returns its value
  screenshot [--file path]         capture the viewport as PNG (default under the temp dir), returns the path
  console [--tail N]               last captured console messages (errors included)
  network [--tail N]               last captured network responses (method, status, url)
  back                             history back
  status                           whether a browser session is running and what page it is on
  close                            close the browser session

Workflow: open -> snapshot -> click/type (by [n] ref) -> snapshot again; use console/network to debug.`
}

// Version returns the plugin contract version.
func (*BuiltinBrowserPlugin) Version() string { return "1.0.0" }

// Path identifies the plugin as builtin.
func (*BuiltinBrowserPlugin) Path() string { return "[builtin]" }

// Schema declares the machine-readable command surface.
func (*BuiltinBrowserPlugin) Schema() string {
	schema := map[string]interface{}{
		"name":        "@browser",
		"description": "Drive a real local browser over the DevTools protocol: navigate, snapshot, click, type, eval, screenshot, console, network.",
		"commands": []map[string]interface{}{
			{"name": "open", "description": "navigate to a URL and return a page snapshot", "examples": []string{`{"cmd":"open","args":{"url":"http://localhost:3000"}}`}},
			{"name": "snapshot", "description": "current page as text with numbered interactive elements", "examples": []string{`{"cmd":"snapshot"}`}},
			{"name": "click", "description": "click an element by snapshot ref or CSS selector", "examples": []string{`{"cmd":"click","args":{"target":"3"}}`, `{"cmd":"click","args":{"target":"#submit"}}`}},
			{"name": "type", "description": "type into an input; submit optionally presses Enter", "examples": []string{`{"cmd":"type","args":{"target":"2","text":"golang","submit":true}}`}},
			{"name": "scroll", "description": "scroll the viewport (down|up|top|bottom) or to a target", "examples": []string{`{"cmd":"scroll","args":{"direction":"down"}}`}},
			{"name": "eval", "description": "run a JavaScript expression in the page", "examples": []string{`{"cmd":"eval","args":{"js":"document.querySelectorAll('li').length"}}`}},
			{"name": "screenshot", "description": "capture the viewport as PNG", "examples": []string{`{"cmd":"screenshot"}`}},
			{"name": "console", "description": "last captured console messages", "examples": []string{`{"cmd":"console","args":{"tail":20}}`}},
			{"name": "network", "description": "last captured network responses", "examples": []string{`{"cmd":"network","args":{"tail":20}}`}},
			{"name": "back", "description": "history back", "examples": []string{`{"cmd":"back"}`}},
			{"name": "status", "description": "session state and current page", "examples": []string{`{"cmd":"status"}`}},
			{"name": "close", "description": "close the browser session", "examples": []string{`{"cmd":"close"}`}},
		},
	}
	b, _ := json.MarshalIndent(schema, "", "  ")
	return string(b)
}

// browserInvocation is the parsed form of one @browser call.
type browserInvocation struct {
	cmd    string
	url    string
	target string
	text   string
	js     string
	file   string
	dir    string
	submit bool
	tail   int
	max    int
}

// Model-facing result strings, named per house style (never inline literals).
const (
	browserMsgClosed        = "Browser session closed."
	browserMsgScrolled      = "Scrolled."
	browserMsgNoValue       = "(no value)"
	browserMsgNoConsole     = "No console messages captured on this page."
	browserMsgNoNetwork     = "No network responses captured on this page."
	browserMsgNotRunning    = "No browser session running — `open` launches one."
	browserMsgNoIdentity    = "Browser session running (page identity unavailable)."
	browserMsgTypedFmt      = "Typed %q into %s."
	browserMsgScreenshotFmt = "Screenshot saved to %s"
	browserMsgBackFmt       = "Went back to: %s (%s)"
	browserMsgRunningFmt    = "Browser session running on: %s (%s)"
)

// Execute dispatches a @browser invocation.
func (p *BuiltinBrowserPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream dispatches a @browser invocation (no streaming — every
// action returns one bounded result).
func (p *BuiltinBrowserPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	inv, err := parseBrowserInvocation(args)
	if err != nil {
		return "", err
	}

	if inv.cmd == "close" {
		browser.Shutdown(ctx)
		return browserMsgClosed, nil
	}
	if inv.cmd == "status" {
		return browserStatus(ctx)
	}

	timeout := browserOpTimeout
	if inv.cmd == "open" {
		timeout = browserOpenTimeout
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	b, err := acquireBrowser(opCtx)
	if err != nil {
		return "", fmt.Errorf("@browser: %w", err)
	}

	switch inv.cmd {
	case "open":
		return browserCmdOpen(opCtx, b, inv)
	case "snapshot":
		return b.Snapshot(opCtx, inv.max)
	case "click":
		return browserCmdClick(opCtx, b, inv)
	case "type":
		return browserCmdType(opCtx, b, inv)
	case "scroll":
		if err := b.Scroll(opCtx, inv.dir, inv.target); err != nil {
			return "", fmt.Errorf("@browser scroll: %w", err)
		}
		return browserMsgScrolled, nil
	case "eval":
		return browserCmdEval(opCtx, b, inv)
	case "screenshot":
		return browserCmdScreenshot(opCtx, b, inv)
	case "console":
		return renderConsoleEntries(b.ConsoleTail(inv.tail)), nil
	case "network":
		return renderNetworkEntries(b.NetworkTail(inv.tail)), nil
	case "back":
		title, url, err := b.Back(opCtx)
		if err != nil {
			return "", fmt.Errorf("@browser back: %w", err)
		}
		return fmt.Sprintf(browserMsgBackFmt, title, url), nil
	default:
		return "", fmt.Errorf("@browser: unknown cmd %q (valid: open|snapshot|click|type|scroll|eval|screenshot|console|network|back|status|close)", inv.cmd)
	}
}

// browserCmdOpen navigates and returns the landing snapshot.
func browserCmdOpen(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	if inv.url == "" {
		return "", errors.New(`@browser open: missing url. Example: {"cmd":"open","args":{"url":"http://localhost:3000"}}`)
	}
	if !strings.Contains(inv.url, "://") {
		inv.url = "https://" + inv.url
	}
	if _, _, err := b.Navigate(ctx, inv.url); err != nil {
		return "", fmt.Errorf("@browser open: %w", err)
	}
	return b.Snapshot(ctx, inv.max)
}

// browserCmdClick clicks and returns the resulting page (the click may have
// navigated or mutated it).
func browserCmdClick(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	if inv.target == "" {
		return "", errors.New(`@browser click: missing target — a [n] ref from the last snapshot or a CSS selector`)
	}
	if err := b.Click(ctx, inv.target); err != nil {
		return "", fmt.Errorf("@browser click: %w", err)
	}
	time.Sleep(600 * time.Millisecond)
	return b.Snapshot(ctx, inv.max)
}

// browserCmdType types into an input; with submit it also shows the page the
// submission produced.
func browserCmdType(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	if inv.target == "" {
		return "", errors.New(`@browser type: missing target — a [n] ref from the last snapshot or a CSS selector`)
	}
	if err := b.Type(ctx, inv.target, inv.text, inv.submit); err != nil {
		return "", fmt.Errorf("@browser type: %w", err)
	}
	if inv.submit {
		time.Sleep(800 * time.Millisecond)
		return b.Snapshot(ctx, inv.max)
	}
	return fmt.Sprintf(browserMsgTypedFmt, inv.text, inv.target), nil
}

// browserCmdEval evaluates a JS expression with a bounded result.
func browserCmdEval(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	if strings.TrimSpace(inv.js) == "" {
		return "", errors.New(`@browser eval: missing js expression`)
	}
	out, err := b.Eval(ctx, inv.js)
	if err != nil {
		return "", fmt.Errorf("@browser eval: %w", err)
	}
	if len(out) > 8000 {
		out = out[:8000] + "\n… (result truncated)"
	}
	if strings.TrimSpace(out) == "" {
		out = browserMsgNoValue
	}
	return out, nil
}

// browserCmdScreenshot captures the viewport, defaulting the path under the
// temp dir.
func browserCmdScreenshot(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	path := inv.file
	if path == "" {
		path = filepath.Join(os.TempDir(), "chatcli-browser",
			fmt.Sprintf("screenshot-%d.png", time.Now().UnixMilli()))
	}
	if err := b.Screenshot(ctx, path); err != nil {
		return "", fmt.Errorf("@browser screenshot: %w", err)
	}
	return fmt.Sprintf(browserMsgScreenshotFmt, path), nil
}

// renderConsoleEntries formats the console tail for the model.
func renderConsoleEntries(entries []browser.ConsoleEntry) string {
	if len(entries) == 0 {
		return browserMsgNoConsole
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Last %d console message(s):\n", len(entries))
	for _, e := range entries {
		fmt.Fprintf(&sb, "[%s] %s\n", e.Kind, e.Text)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderNetworkEntries formats the network tail for the model.
func renderNetworkEntries(entries []browser.NetworkEntry) string {
	if len(entries) == 0 {
		return browserMsgNoNetwork
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Last %d network response(s):\n", len(entries))
	for _, e := range entries {
		fmt.Fprintf(&sb, "%d %s %s (%s)\n", e.Status, e.Method, e.URL, e.Type)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// browserStatus reports the session state without launching a browser.
// Package variable so tests can exercise dispatch without pkg/browser state.
var browserStatus = func(ctx context.Context) (string, error) {
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	running, title, url := browser.DefaultStatus(opCtx)
	if !running {
		return browserMsgNotRunning, nil
	}
	if title == "" && url == "" {
		return browserMsgNoIdentity, nil
	}
	return fmt.Sprintf(browserMsgRunningFmt, title, url), nil
}

// parseBrowserInvocation understands the JSON envelope and flat argv forms,
// leniently — strict parsing makes the model fail and retry.
func parseBrowserInvocation(args []string) (browserInvocation, error) {
	inv := browserInvocation{tail: browserDefaultTail}
	if len(args) == 0 {
		return inv, errors.New(`@browser: empty args. Example: <tool_call name="@browser" args='{"cmd":"open","args":{"url":"http://localhost:3000"}}' />`)
	}

	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		return parseBrowserEnvelope(payload, inv)
	}

	inv.cmd = strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]

	// Flags anywhere in the tail; positionals fill the per-command slots.
	var positionals []string
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		next := func() string {
			if i+1 < len(rest) {
				i++
				return rest[i]
			}
			return ""
		}
		switch {
		case a == "--submit":
			inv.submit = true
		case a == "--file":
			inv.file = next()
		case strings.HasPrefix(a, "--file="):
			inv.file = strings.TrimPrefix(a, "--file=")
		case a == "--tail":
			inv.tail = atoiDefault(next(), browserDefaultTail)
		case strings.HasPrefix(a, "--tail="):
			inv.tail = atoiDefault(strings.TrimPrefix(a, "--tail="), browserDefaultTail)
		case a == "--max":
			inv.max = atoiDefault(next(), 0)
		case strings.HasPrefix(a, "--max="):
			inv.max = atoiDefault(strings.TrimPrefix(a, "--max="), 0)
		case a == "--to":
			inv.target = next()
		case strings.HasPrefix(a, "--to="):
			inv.target = strings.TrimPrefix(a, "--to=")
		default:
			positionals = append(positionals, a)
		}
	}

	switch inv.cmd {
	case "open":
		if len(positionals) > 0 {
			inv.url = positionals[0]
		}
	case "click":
		if len(positionals) > 0 {
			inv.target = positionals[0]
		}
	case "type":
		if len(positionals) > 0 {
			inv.target = positionals[0]
		}
		if len(positionals) > 1 {
			inv.text = strings.Join(positionals[1:], " ")
		}
	case "eval":
		inv.js = strings.Join(positionals, " ")
	case "scroll":
		if len(positionals) > 0 {
			inv.dir = strings.ToLower(positionals[0])
		}
	case "console", "network":
		if len(positionals) > 0 {
			inv.tail = atoiDefault(positionals[0], browserDefaultTail)
		}
	}
	return inv, nil
}

// parseBrowserEnvelope handles {"cmd":..., "args":{...}} (args optionally
// inlined at the top level — models flatten envelopes all the time).
func parseBrowserEnvelope(payload string, inv browserInvocation) (browserInvocation, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return inv, fmt.Errorf(`@browser: parse envelope: %w. Expected {"cmd":"open","args":{"url":"..."}}`, err)
	}
	var cmd string
	if rc, ok := raw["cmd"]; ok {
		_ = json.Unmarshal(rc, &cmd)
	}
	inv.cmd = strings.ToLower(strings.TrimSpace(cmd))
	inner := raw
	if ra, ok := raw["args"]; ok && len(ra) > 0 && strings.TrimSpace(string(ra)) != "null" {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(ra, &nested); err == nil {
			inner = nested
		}
	}
	getStr := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := inner[k]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil {
					return s
				}
				// Numbers arrive unquoted ({"target": 3}); render them.
				var n float64
				if json.Unmarshal(v, &n) == nil {
					return strconv.Itoa(int(n))
				}
			}
		}
		return ""
	}
	getInt := func(key string, def int) int {
		if v, ok := inner[key]; ok {
			var n int
			if json.Unmarshal(v, &n) == nil {
				return n
			}
		}
		return def
	}
	getBool := func(key string) bool {
		if v, ok := inner[key]; ok {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				return b
			}
		}
		return false
	}

	inv.url = getStr("url", "href")
	inv.target = getStr("target", "selector", "ref", "to")
	inv.text = getStr("text", "value")
	inv.js = getStr("js", "expression", "script", "code")
	inv.file = getStr("file", "path")
	inv.dir = getStr("direction", "dir")
	inv.submit = getBool("submit")
	inv.tail = getInt("tail", browserDefaultTail)
	inv.max = getInt("max", 0)
	return inv, nil
}

// atoiDefault parses n leniently, falling back to def.
func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
