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
 * Visibility is per call, not only per process: `show` / `open --visible`
 * surface the window on the user's screen (relaunching the headless browser
 * on the same profile, so cookies survive), `hide` takes it back, and
 * `wait` blocks until the page reaches a URL/text — the hand-off the user
 * needs to log in, solve a captcha or pick an account themselves, after
 * which the agent keeps driving the authenticated session.
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

// browserIdentity is the optional backend surface for a cheap title/URL
// probe (used by `wait` instead of a full snapshot per poll).
type browserIdentity interface {
	Identity(ctx context.Context) (title, url string, err error)
}

// acquireBrowser returns the live backend in the requested visibility,
// launching (or relaunching) the browser as needed. Package variable so
// tests inject a fake without a real Chrome.
var acquireBrowser = func(ctx context.Context, mode browser.Mode) (BrowserBackend, error) {
	return browser.AcquireMode(ctx, mode)
}

const (
	browserOpenTimeout = 60 * time.Second // may include a cold browser launch
	browserOpTimeout   = 25 * time.Second
	browserDefaultTail = 30
	// browserWaitDefault / browserWaitMax bound `wait`: long enough for a
	// human login, short enough that a forgotten wait never hangs the turn.
	browserWaitDefault = 120 * time.Second
	browserWaitMax     = 600 * time.Second
	browserWaitPoll    = 1 * time.Second
)

// BuiltinBrowserPlugin is the @browser tool.
type BuiltinBrowserPlugin struct{}

// NewBuiltinBrowserPlugin returns a ready-to-register plugin.
func NewBuiltinBrowserPlugin() *BuiltinBrowserPlugin { return &BuiltinBrowserPlugin{} }

// Name returns "@browser".
func (*BuiltinBrowserPlugin) Name() string { return "@browser" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinBrowserPlugin) Description() string {
	return "Drive a real local Chrome/Chromium browser: open a URL, read the rendered page as text with numbered interactive elements, click and type into them, run JavaScript, capture screenshots, and inspect the page's console messages and network responses. Use it to VERIFY web work — open the app you just built or changed, interact with it, and debug it from its console/network activity. The browser runs headless by default; when the user must act in person (log in, pick an account, solve a captcha, or simply see the page), use `show` or `open --visible` to put the window on their screen, `wait` until the page reaches the expected URL/text, then `hide` and keep going — the session and its logins are kept across the switch. Requires a locally installed Chrome, Chromium, Brave or Edge."
}

// Usage explains the canonical invocation forms.
func (*BuiltinBrowserPlugin) Usage() string {
	return `<tool_call name="@browser" args='{"cmd":"open","args":{"url":"http://localhost:3000"}}' />
<tool_call name="@browser" args='{"cmd":"click","args":{"target":"3"}}' />
<tool_call name="@browser" args='{"cmd":"show","args":{"url":"https://app.example.com/login"}}' />
<tool_call name="@browser" args='{"cmd":"wait","args":{"url":"/dashboard","timeout":300}}' />

Subcommands:
  open {url} [--visible]           navigate to url (launches the browser on first use), returns a page snapshot; --visible puts the window on the user's screen
  show [url]                       make the browser window VISIBLE on the user's screen (relaunches a headless session on the same profile — logins are kept); optionally navigates to url
  hide                             back to headless (window disappears, session and cookies are kept)
  wait [--url substr] [--text substr] [--selector css|ref] [--changed] [--timeout secs]   block until the page URL/text contains substr, an element exists and/or the URL leaves the current one (default 120s, max 600s) — pair with show for a user login; --changed when you cannot predict the landing page
  snapshot [--max N]               current page as text: title, url, numbered interactive elements, visible text
  click {target}                   click an element — target is a [n] ref from the last snapshot or a CSS selector
  type {target} {text} [--submit]  type into an input; --submit presses Enter / submits its form
  press {key}                      send a key to the focused element: Enter, Tab, Escape, ArrowDown, a character, or a chord like Control+a
  hover {target}                   move the mouse over an element (opens hover menus/tooltips)
  select {target} {value}          choose a <select> option by value or visible text
  upload {target} {file}           attach a local file to an <input type=file> (the OS picker never opens under automation)
  scroll [down|up|top|bottom|--to target]   move the viewport
  eval {javascript}                run a JS expression in the page, returns its value
  screenshot [--file path] [--full]   capture the viewport (or the whole page with --full) as PNG, returns the path
  html [target] [--max N]          outerHTML of an element (or the document) — for debugging markup
  pdf [--file path]                save the page as PDF (headless only)
  resize {width} {height} [--mobile]   emulate a viewport (e.g. 390 844 --mobile) to verify responsive layouts
  tabs                             list open tabs (popups such as OAuth windows appear here)
  tab {n}                          switch to tab n from the tabs list
  cookies [domain] [--clear]       list cookies (names/domains only, never values) to check whether a login stuck; --clear logs out of everything
  console [--tail N]               last captured console messages (errors and auto-accepted dialogs included)
  network [--tail N]               last captured network responses (method, status, url)
  back                             history back
  status                           whether a browser session is running, visible or headless, and what page it is on
  close                            close the browser session

Workflow: open -> snapshot -> click/type (by [n] ref) -> snapshot again; use console/network to debug.
User hand-off (login, captcha, account picker): show {login url} -> tell the user what to do -> wait --url {post-login path} (or ask them with @ask) -> hide -> continue on the authenticated session.
alert()/confirm()/prompt() dialogs are accepted automatically and logged to console.
The browser profile is a throwaway unless CHATCLI_BROWSER_PROFILE is set; the user's everyday Chrome logins are only available when CHATCLI_BROWSER_CDP_URL attaches to their running browser.`
}

// Version returns the plugin contract version.
func (*BuiltinBrowserPlugin) Version() string { return "1.0.0" }

// Path identifies the plugin as builtin.
func (*BuiltinBrowserPlugin) Path() string { return "[builtin]" }

// Schema declares the machine-readable command surface.
func (*BuiltinBrowserPlugin) Schema() string {
	schema := map[string]interface{}{
		"name":        "@browser",
		"description": "Drive a real local browser over the DevTools protocol.",
		"argsFormat":  "JSON envelope {cmd, args} preferred (e.g. {\"cmd\":\"open\",\"args\":{\"url\":\"...\"}}); flat argv also accepted.",
		"subcommands": []map[string]interface{}{
			{"name": "open", "description": "navigate to a URL and return a page snapshot; visible:true puts the window on the user's screen", "examples": []string{`{"cmd":"open","args":{"url":"http://localhost:3000"}}`, `{"cmd":"open","args":{"url":"https://app.example.com/login","visible":true}}`}},
			{"name": "show", "description": "make the browser window visible to the user (session and logins kept); optional url to navigate", "examples": []string{`{"cmd":"show"}`, `{"cmd":"show","args":{"url":"https://app.example.com/login"}}`}},
			{"name": "hide", "description": "return to headless (session and logins kept)", "examples": []string{`{"cmd":"hide"}`}},
			{"name": "wait", "description": "block until the page URL/text contains the given substring, an element exists and/or the URL changes (changed:true — for logins whose landing page is unknown), up to timeout seconds (default 120, max 600); a timeout is a result with the current page", "examples": []string{`{"cmd":"wait","args":{"url":"/dashboard","timeout":300}}`, `{"cmd":"wait","args":{"text":"Welcome back"}}`, `{"cmd":"wait","args":{"changed":true,"timeout":300}}`}},
			{"name": "snapshot", "description": "current page as text with numbered interactive elements", "examples": []string{`{"cmd":"snapshot"}`}},
			{"name": "click", "description": "click an element by snapshot ref or CSS selector", "examples": []string{`{"cmd":"click","args":{"target":"3"}}`, `{"cmd":"click","args":{"target":"#submit"}}`}},
			{"name": "type", "description": "type into an input; submit optionally presses Enter", "examples": []string{`{"cmd":"type","args":{"target":"2","text":"golang","submit":true}}`}},
			{"name": "press", "description": "send a key or chord to the focused element (Enter, Tab, Escape, ArrowDown, Control+a, a character)", "examples": []string{`{"cmd":"press","args":{"key":"Enter"}}`, `{"cmd":"press","args":{"key":"Control+a"}}`}},
			{"name": "hover", "description": "move the mouse over an element by snapshot ref or CSS selector", "examples": []string{`{"cmd":"hover","args":{"target":"5"}}`}},
			{"name": "select", "description": "choose a <select> option by value or visible text", "examples": []string{`{"cmd":"select","args":{"target":"4","value":"BR"}}`}},
			{"name": "upload", "description": "attach local file(s) to an <input type=file>", "examples": []string{`{"cmd":"upload","args":{"target":"2","file":"/tmp/report.pdf"}}`, `{"cmd":"upload","args":{"target":"2","files":["/tmp/a.png","/tmp/b.png"]}}`}},
			{"name": "scroll", "description": "scroll the viewport (down|up|top|bottom) or to a target", "examples": []string{`{"cmd":"scroll","args":{"direction":"down"}}`}},
			{"name": "eval", "description": "run a JavaScript expression in the page", "examples": []string{`{"cmd":"eval","args":{"js":"document.querySelectorAll('li').length"}}`}},
			{"name": "screenshot", "description": "capture the viewport as PNG; full:true captures the whole page", "examples": []string{`{"cmd":"screenshot"}`, `{"cmd":"screenshot","args":{"full":true,"file":"/tmp/page.png"}}`}},
			{"name": "html", "description": "outerHTML of an element (target) or the whole document, capped at max bytes", "examples": []string{`{"cmd":"html","args":{"target":"#app","max":4000}}`}},
			{"name": "pdf", "description": "save the page as PDF (headless only)", "examples": []string{`{"cmd":"pdf","args":{"file":"/tmp/page.pdf"}}`}},
			{"name": "resize", "description": "emulate a viewport; mobile:true also enables touch", "examples": []string{`{"cmd":"resize","args":{"width":390,"height":844,"mobile":true}}`}},
			{"name": "tabs", "description": "list open tabs (popups included)", "examples": []string{`{"cmd":"tabs"}`}},
			{"name": "tab", "description": "switch to tab n from the tabs list", "examples": []string{`{"cmd":"tab","args":{"target":"2"}}`}},
			{"name": "cookies", "description": "list cookies (names/domains, never values), optionally filtered by domain; clear:true wipes all cookies", "examples": []string{`{"cmd":"cookies","args":{"domain":"example.com"}}`, `{"cmd":"cookies","args":{"clear":true}}`}},
			{"name": "console", "description": "last captured console messages", "examples": []string{`{"cmd":"console","args":{"tail":20}}`}},
			{"name": "network", "description": "last captured network responses", "examples": []string{`{"cmd":"network","args":{"tail":20}}`}},
			{"name": "back", "description": "history back", "examples": []string{`{"cmd":"back"}`}},
			{"name": "status", "description": "session state (visible/headless/attached, profile) and current page", "examples": []string{`{"cmd":"status"}`}},
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
	// visible is the tri-state --visible/--headless request on open:
	// visibleSet=false means "keep whatever is running".
	visibleSet bool
	visible    bool
	// timeout is the `wait` bound in seconds (0 = default).
	timeout int
	// selector is the `wait` element condition.
	selector string
	// changed is the `wait` condition "URL left the one it had at start".
	changed bool
	// Second-tier verbs.
	key    string   // press
	files  []string // upload
	width  int      // resize
	height int      // resize
	mobile bool     // resize
	full   bool     // screenshot --full
	clear  bool     // cookies --clear
}

// mode maps the invocation's visibility request onto the session mode.
func (inv browserInvocation) mode() browser.Mode {
	switch inv.cmd {
	case "show":
		return browser.ModeVisible
	case "hide":
		return browser.ModeHeadless
	}
	if !inv.visibleSet {
		return browser.ModeDefault
	}
	if inv.visible {
		return browser.ModeVisible
	}
	return browser.ModeHeadless
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
	browserMsgVisibleTag    = " [visible window]"
	browserMsgHeadlessTag   = " [headless]"
	browserMsgAttachedTag   = " [attached to the user's own browser — window is theirs, always visible]"
	browserMsgProfileFmt    = " [persistent profile: %s]"
	browserMsgShown         = "Browser window is now VISIBLE on the user's screen. They can log in or interact directly; the session (cookies, logins) is shared with you. Use `wait` (by URL/text) or ask the user when they are done, then `hide` to go back to headless."
	browserMsgHidden        = "Browser window hidden (headless again). The session and its logins are kept."
	browserMsgHideAttached  = "This session drives the user's own browser (CHATCLI_BROWSER_CDP_URL); its window stays as it is."
	browserMsgWaitDoneFmt   = "Condition met after %s: %s (%s)"
	browserMsgWaitTimeout   = "Timed out after %s waiting for %s; page is still: %s (%s). The user may still be busy — ask them, or wait again."
	browserMsgWaitMovedFmt  = " The page did move during the wait, from %s — the user may already be done (check the snapshot) even though the exact condition did not match."
	browserMsgPageClosed    = "The browser page was closed before the condition was met — the user closed the tab or window (a site may also have refused to proceed; ask them what they saw). The session is still running: the next open/show attaches a fresh tab."
	browserMsgPageClosedTag = " [page closed by the user — next open/show attaches a fresh tab]"
	browserMsgBrowserClosed = "The browser was closed before the condition was met — the user quit it. The next open/show launches a new browser; logins made in a throwaway profile are gone (CHATCLI_BROWSER_PROFILE keeps them)."
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
	if inv.cmd == "hide" && !browserAlive() {
		// Nothing to hide — and acquiring would launch a headless browser
		// for no reason.
		return browserMsgNotRunning, nil
	}

	timeout := browserOpTimeout
	switch inv.cmd {
	case "open", "show", "hide":
		timeout = browserOpenTimeout // may include a (re)launch
	case "wait":
		timeout = browserWaitBound(inv.timeout) + browserOpTimeout
	}
	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	b, err := acquireBrowser(opCtx, inv.mode())
	if err != nil {
		return "", fmt.Errorf("@browser: %w", err)
	}

	switch inv.cmd {
	case "open":
		return browserCmdOpen(opCtx, b, inv)
	case "show":
		return browserCmdShow(opCtx, b, inv)
	case "hide":
		if att, ok := b.(interface{ Attached() bool }); ok && att.Attached() {
			return browserMsgHideAttached, nil
		}
		return browserMsgHidden, nil
	case "wait":
		return browserCmdWait(opCtx, b, inv)
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
	case "press", "hover", "select", "upload", "html", "pdf", "resize", "tabs", "tab", "cookies":
		return browserCmdExt(opCtx, b, inv)
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
		return "", fmt.Errorf("@browser: unknown cmd %q (valid: open|show|hide|wait|snapshot|click|type|press|hover|select|upload|scroll|eval|screenshot|html|pdf|resize|tabs|tab|cookies|console|network|back|status|close)", inv.cmd)
	}
}

// browserCmdShow surfaces the window (acquire already relaunched it visible)
// and optionally navigates, returning the hand-off note plus the page.
func browserCmdShow(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	if inv.url != "" {
		snap, err := browserCmdOpen(ctx, b, inv)
		if err != nil {
			return "", err
		}
		return browserMsgShown + "\n\n" + snap, nil
	}
	return browserMsgShown, nil
}

// browserWaitBound clamps the requested wait seconds into [default, max].
func browserWaitBound(seconds int) time.Duration {
	if seconds <= 0 {
		return browserWaitDefault
	}
	d := time.Duration(seconds) * time.Second
	if d > browserWaitMax {
		return browserWaitMax
	}
	return d
}

// browserCmdWait polls the page until its URL and/or visible text contains
// the requested substrings, or the bound elapses. Both conditions, when
// given, must hold. A timeout is a result, not an error: the model needs the
// page state to decide whether to ask the user or wait again.
func browserCmdWait(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	urlSub, textSub, selector := strings.TrimSpace(inv.url), strings.TrimSpace(inv.text), strings.TrimSpace(inv.selector)
	if urlSub == "" && textSub == "" && selector == "" && !inv.changed {
		return "", errors.New(`@browser wait: give a condition — {"cmd":"wait","args":{"url":"/dashboard"}}, {"text":"Welcome"}, {"selector":"#app-ready"} and/or {"changed":true} (URL leaves the current one — for logins whose landing page you cannot predict); to wait for the user without a page condition, ask them with @ask instead`)
	}
	bound := browserWaitBound(inv.timeout)
	start := time.Now()
	deadline := start.Add(bound)
	var title, url, startURL string
	first := true
	for {
		var err error
		title, url, err = browserPageState(ctx, b, textSub != "")
		if errors.Is(err, browser.ErrPageClosed) {
			return browserMsgPageClosed, nil
		}
		if errors.Is(err, browser.ErrBrowserClosed) {
			return browserMsgBrowserClosed, nil
		}
		if err != nil {
			return "", fmt.Errorf("@browser wait: %w", err)
		}
		if first {
			startURL, first = url, false
			if inv.changed {
				// The starting page is by definition not the destination.
				if !browserWaitSleep(ctx) {
					return "", fmt.Errorf("@browser wait: %w", ctx.Err())
				}
				continue
			}
		}
		ok := browserWaitSatisfied(url, title, urlSub, textSub)
		if ok && inv.changed && url == startURL {
			ok = false
		}
		if ok && selector != "" {
			ok, err = browserSelectorPresent(ctx, b, selector)
			if err != nil {
				return "", fmt.Errorf("@browser wait: %w", err)
			}
		}
		if ok {
			return fmt.Sprintf(browserMsgWaitDoneFmt, time.Since(start).Round(time.Second), browserWaitTitle(title), url), nil
		}
		if time.Now().After(deadline) {
			break
		}
		if !browserWaitSleep(ctx) {
			return "", fmt.Errorf("@browser wait: %w", ctx.Err())
		}
	}
	msg := fmt.Sprintf(browserMsgWaitTimeout, bound, browserWaitCondition(urlSub, textSub, selector, inv.changed), browserWaitTitle(title), url)
	if startURL != "" && url != startURL {
		msg += fmt.Sprintf(browserMsgWaitMovedFmt, startURL)
	}
	return msg, nil
}

// browserWaitSleep pauses one poll interval; false when ctx ended first.
func browserWaitSleep(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(browserWaitPoll):
		return true
	}
}

// browserSelectorPresent reports whether a visible element matches selector
// (a [n] ref or CSS), through the plain Eval every backend has.
func browserSelectorPresent(ctx context.Context, b BrowserBackend, selector string) (bool, error) {
	selJSON, _ := json.Marshal(browserResolveWaitSelector(selector))
	out, err := b.Eval(ctx, fmt.Sprintf(`(() => { const el = document.querySelector(%s); if (!el) return false; const r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0; })()`, selJSON))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// browserResolveWaitSelector maps a bare [n] ref onto its stamped selector
// (mirrors pkg/browser.resolveSelector without exporting it).
func browserResolveWaitSelector(target string) string {
	t := strings.Trim(strings.TrimSpace(target), "[]")
	if t != "" && strings.Trim(t, "0123456789") == "" {
		return fmt.Sprintf(`[data-chatcli-ref="%s"]`, t)
	}
	return strings.TrimSpace(target)
}

// browserPageState returns the page title (or, when withText, the whole
// snapshot text so a text condition can be matched) and URL.
func browserPageState(ctx context.Context, b BrowserBackend, withText bool) (title, url string, err error) {
	if !withText {
		if ip, ok := b.(browserIdentity); ok {
			return ip.Identity(ctx)
		}
	}
	snap, err := b.Snapshot(ctx, 0)
	if err != nil {
		return "", "", err
	}
	return snap, browserSnapshotURL(snap), nil
}

// browserSnapshotURL extracts the "URL: …" line a snapshot starts with.
func browserSnapshotURL(snap string) string {
	for _, line := range strings.Split(snap, "\n") {
		if strings.HasPrefix(line, "URL: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "URL: "))
		}
	}
	return ""
}

// browserWaitSatisfied applies the wait conditions to the current state.
func browserWaitSatisfied(url, text, urlSub, textSub string) bool {
	if urlSub != "" && !strings.Contains(strings.ToLower(url), strings.ToLower(urlSub)) {
		return false
	}
	if textSub != "" && !strings.Contains(strings.ToLower(text), strings.ToLower(textSub)) {
		return false
	}
	return true
}

// browserWaitTitle reduces a state string (title or full snapshot) to the
// page title for the result line.
func browserWaitTitle(state string) string {
	for _, line := range strings.Split(state, "\n") {
		if strings.HasPrefix(line, "Page: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Page: "))
		}
	}
	if i := strings.IndexByte(state, '\n'); i >= 0 {
		return strings.TrimSpace(state[:i])
	}
	return strings.TrimSpace(state)
}

// browserWaitCondition renders the condition for the timeout message.
func browserWaitCondition(urlSub, textSub, selector string, changed bool) string {
	var parts []string
	if changed {
		parts = append(parts, "url to change")
	}
	if urlSub != "" {
		parts = append(parts, fmt.Sprintf("url containing %q", urlSub))
	}
	if textSub != "" {
		parts = append(parts, fmt.Sprintf("text containing %q", textSub))
	}
	if selector != "" {
		parts = append(parts, fmt.Sprintf("element %q", selector))
	}
	return strings.Join(parts, " and ")
}

// browserCmdOpen navigates and returns the landing snapshot.
func browserCmdOpen(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	if inv.url == "" {
		return "", errors.New(`@browser open: missing url. Example: {"cmd":"open","args":{"url":"http://localhost:3000"}}`)
	}
	inv.url = browserNormalizeURL(inv.url)
	if _, _, err := b.Navigate(ctx, inv.url); err != nil {
		return "", fmt.Errorf("@browser open: %w", err)
	}
	return b.Snapshot(ctx, inv.max)
}

// browserNormalizeURL defaults a bare host to https:// while leaving
// scheme-carrying URLs (about:blank, data:, file:, javascript:) alone.
func browserNormalizeURL(raw string) string {
	u := strings.TrimSpace(raw)
	if strings.Contains(u, "://") {
		return u
	}
	for _, scheme := range []string{"about:", "data:", "file:", "javascript:", "blob:", "chrome:"} {
		if strings.HasPrefix(strings.ToLower(u), scheme) {
			return u
		}
	}
	return "https://" + u
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
	if inv.full {
		ext, err := browserExt(b, "screenshot --full")
		if err != nil {
			return "", err
		}
		if err := ext.ScreenshotFull(ctx, path); err != nil {
			return "", fmt.Errorf("@browser screenshot: %w", err)
		}
		return fmt.Sprintf(browserMsgScreenshotFmt, path), nil
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

// browserAlive reports whether a session is running. Package variable so
// tests can exercise dispatch without pkg/browser state.
var browserAlive = browser.DefaultAlive

// browserStatus reports the session state without launching a browser.
// Package variable so tests can exercise dispatch without pkg/browser state.
var browserStatus = func(ctx context.Context) (string, error) {
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	running, title, url := browser.DefaultStatus(opCtx)
	if !running {
		return browserMsgNotRunning, nil
	}
	tag := browserMsgHeadlessTag
	if browser.DefaultVisible() {
		tag = browserMsgVisibleTag
	}
	if browser.DefaultAttached() {
		tag = browserMsgAttachedTag
	} else if dir, persistent := browser.DefaultProfile(); persistent {
		tag += fmt.Sprintf(browserMsgProfileFmt, dir)
	}
	if browser.DefaultPageClosed() {
		tag += browserMsgPageClosedTag
	}
	if title == "" && url == "" {
		return browserMsgNoIdentity + tag, nil
	}
	return fmt.Sprintf(browserMsgRunningFmt, title, url) + tag, nil
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

	// The agent loop flattens a JSON envelope {"cmd":"open","args":{"url":X}}
	// into argv ["open","--url",X], so every args-map key arrives as a
	// `--flag value` (or `--flag=value`) pair. splitFlatArgs collects those
	// generically (an unknown --flag is kept, not mistaken for a positional),
	// then the keys are mapped onto the invocation. A strict parser that
	// ignored these flags mistook "--url" itself for the URL.
	flags, bools, positionals := splitFlatArgs(rest)
	positionals = inv.applyFlatFlags(flags, bools, positionals)
	inv.applyPositionals(positionals)
	return inv, nil
}

// applyFlatFlags maps the --key value pairs of a flattened envelope onto the
// invocation and returns the positionals (visibility flags may hand one
// back, see applyVisibility).
func (inv *browserInvocation) applyFlatFlags(flags map[string]string, bools map[string]bool, positionals []string) []string {
	inv.submit = bools["submit"]
	positionals = inv.applyVisibility(flags, bools, positionals)
	if v := firstFlag(flags, "timeout", "seconds", "secs"); v != "" {
		inv.timeout = atoiDefault(v, 0)
	}
	inv.selector = firstFlag(flags, "selector", "element", "for")
	inv.changed = bools["changed"] || bools["navigated"] || boolFlag(flags, "changed") || boolFlag(flags, "navigated")
	inv.key = firstFlag(flags, "key", "keys", "chord")
	if v := firstFlag(flags, "width", "w"); v != "" {
		inv.width = atoiDefault(v, 0)
	}
	if v := firstFlag(flags, "height", "h"); v != "" {
		inv.height = atoiDefault(v, 0)
	}
	inv.mobile = bools["mobile"] || boolFlag(flags, "mobile")
	inv.full = bools["full"] || bools["fullpage"] || boolFlag(flags, "full") || boolFlag(flags, "fullpage")
	inv.clear = bools["clear"] || boolFlag(flags, "clear")
	inv.file = firstFlag(flags, "file", "path")
	if v := firstFlag(flags, "files"); v != "" {
		inv.files = splitFileList(v)
	} else if inv.file != "" {
		inv.files = []string{inv.file}
	}
	inv.url = firstFlag(flags, "url", "href")
	inv.target = firstFlag(flags, "target", "selector", "ref", "to")
	inv.text = firstFlag(flags, "text", "value")
	if v := firstFlag(flags, "domain"); v != "" && inv.text == "" {
		inv.text = v
	}
	inv.js = firstFlag(flags, "js", "expression", "script", "code")
	if v := firstFlag(flags, "direction", "dir"); v != "" {
		inv.dir = strings.ToLower(v)
	}
	if v := firstFlag(flags, "tail"); v != "" {
		inv.tail = atoiDefault(v, browserDefaultTail)
	}
	if v := firstFlag(flags, "max"); v != "" {
		inv.max = atoiDefault(v, 0)
	}

	return positionals
}

// applyPositionals reads the bare tokens per command: `open URL`,
// `click 3`, `type 2 hello world`, `press Enter`, `resize 1280 800`…
func (inv *browserInvocation) applyPositionals(positionals []string) {
	if inv.applyExtPositionals(positionals) {
		return
	}
	switch inv.cmd {
	case "open", "show":
		if inv.url == "" && len(positionals) > 0 {
			inv.url = positionals[0]
		}
	case "click":
		if inv.target == "" && len(positionals) > 0 {
			inv.target = positionals[0]
		}
	case "type":
		if inv.target == "" && len(positionals) > 0 {
			inv.target = positionals[0]
			positionals = positionals[1:]
		}
		if inv.text == "" && len(positionals) > 0 {
			inv.text = strings.Join(positionals, " ")
		}
	case "eval":
		if inv.js == "" {
			inv.js = strings.Join(positionals, " ")
		}
	case "scroll":
		if inv.dir == "" && len(positionals) > 0 {
			inv.dir = strings.ToLower(positionals[0])
		}
	case "console", "network":
		if len(positionals) > 0 {
			inv.tail = atoiDefault(positionals[0], browserDefaultTail)
		}
	}
}

// extPositionalReaders map wait and the second-tier verbs onto their
// positional readers, one small function each.
var extPositionalReaders = map[string]func(*browserInvocation, []string){
	"wait":       (*browserInvocation).posWait,
	"press":      (*browserInvocation).posPress,
	"hover":      (*browserInvocation).posTarget,
	"tab":        (*browserInvocation).posTarget,
	"html":       (*browserInvocation).posTarget,
	"select":     (*browserInvocation).posTargetText,
	"upload":     (*browserInvocation).posUpload,
	"resize":     (*browserInvocation).posResize,
	"pdf":        (*browserInvocation).posFile,
	"cookies":    (*browserInvocation).posCookies,
	"screenshot": (*browserInvocation).posScreenshot,
}

// applyExtPositionals handles the positionals of wait and the second-tier
// verbs; it reports whether cmd was one of them.
func (inv *browserInvocation) applyExtPositionals(positionals []string) bool {
	read, ok := extPositionalReaders[inv.cmd]
	if !ok {
		return false
	}
	read(inv, positionals)
	return true
}

// posWait: `wait /dashboard` and `wait 300` both read naturally.
func (inv *browserInvocation) posWait(positionals []string) {
	for _, pos := range positionals {
		if n, err := strconv.Atoi(pos); err == nil && inv.timeout == 0 {
			inv.timeout = n
		} else if strings.EqualFold(pos, "changed") || strings.EqualFold(pos, "navigated") {
			inv.changed = true
		} else if inv.url == "" && inv.text == "" && inv.selector == "" {
			inv.url = pos
		}
	}
}

// posPress joins `press Control a` into the chord "Control+a".
func (inv *browserInvocation) posPress(positionals []string) {
	if inv.key == "" && len(positionals) > 0 {
		inv.key = strings.Join(positionals, "+")
	}
}

// posTarget reads a single target positional.
func (inv *browserInvocation) posTarget(positionals []string) {
	if inv.target == "" && len(positionals) > 0 {
		inv.target = positionals[0]
	}
}

// posTargetText reads a target followed by free text (select).
func (inv *browserInvocation) posTargetText(positionals []string) {
	if inv.target == "" && len(positionals) > 0 {
		inv.target = positionals[0]
		positionals = positionals[1:]
	}
	if inv.text == "" && len(positionals) > 0 {
		inv.text = strings.Join(positionals, " ")
	}
}

// posUpload reads a target followed by one or more file paths.
func (inv *browserInvocation) posUpload(positionals []string) {
	if inv.target == "" && len(positionals) > 0 {
		inv.target = positionals[0]
		positionals = positionals[1:]
	}
	if len(inv.files) == 0 && len(positionals) > 0 {
		inv.files = positionals
	}
}

// posResize reads `1280 800`, `1280x800`, optionally followed by `mobile`.
func (inv *browserInvocation) posResize(positionals []string) {
	if inv.width != 0 && inv.height != 0 {
		return
	}
	var rest []string
	inv.width, inv.height, rest = parseDims(positionals)
	for _, r := range rest {
		if strings.EqualFold(r, "mobile") {
			inv.mobile = true
		}
	}
}

// posFile reads a single output path.
func (inv *browserInvocation) posFile(positionals []string) {
	if inv.file == "" && len(positionals) > 0 {
		inv.file = positionals[0]
	}
}

// posCookies reads an optional domain filter and the `clear` word.
func (inv *browserInvocation) posCookies(positionals []string) {
	for _, pos := range positionals {
		if strings.EqualFold(pos, "clear") {
			inv.clear = true
		} else if inv.text == "" {
			inv.text = pos
		}
	}
}

// posScreenshot reads an optional path and the `full` word.
func (inv *browserInvocation) posScreenshot(positionals []string) {
	for _, pos := range positionals {
		if strings.EqualFold(pos, "full") {
			inv.full = true
		} else if inv.file == "" {
			inv.file = pos
		}
	}
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
	inv.timeout = getInt("timeout", getInt("seconds", 0))
	inv.selector = getStr("selector", "element", "for")
	inv.changed = getBool("changed") || getBool("navigated")
	inv.key = getStr("key", "keys", "chord")
	inv.width = getInt("width", getInt("w", 0))
	inv.height = getInt("height", getInt("h", 0))
	inv.mobile = getBool("mobile")
	inv.full = getBool("full") || getBool("fullpage") || getBool("fullPage")
	inv.clear = getBool("clear")
	if v, ok := inner["files"]; ok {
		var list []string
		if json.Unmarshal(v, &list) == nil && len(list) > 0 {
			inv.files = list
		} else if one := getStr("files"); one != "" {
			inv.files = splitFileList(one)
		}
	}
	if len(inv.files) == 0 && inv.file != "" {
		inv.files = []string{inv.file}
	}
	if d := getStr("domain"); d != "" && inv.text == "" {
		inv.text = d
	}
	for _, key := range []string{"visible", "headed", "show"} {
		if v, ok := inner[key]; ok {
			inv.visibleSet, inv.visible = true, boolish(v)
			break
		}
	}
	if !inv.visibleSet {
		if v, ok := inner["headless"]; ok {
			inv.visibleSet, inv.visible = true, !boolish(v)
		}
	}
	return inv, nil
}

// boolFlag reads a `--flag value` boolean from the flat map.
func boolFlag(flags map[string]string, key string) bool {
	v, ok := flags[key]
	if !ok {
		return false
	}
	b, _ := boolWord(v)
	return b
}

// splitFileList splits a comma- or semicolon-separated file list.
func splitFileList(v string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// boolish reads a JSON bool leniently: true/false, "true"/"false", 1/0.
func boolish(v json.RawMessage) bool {
	var b bool
	if json.Unmarshal(v, &b) == nil {
		return b
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "1", "true", "yes", "on":
			return true
		}
		return false
	}
	var n float64
	if json.Unmarshal(v, &n) == nil {
		return n != 0
	}
	return false
}

// applyVisibility reads the flat-argv visibility flags: --visible/--headed
// /--show and --headless, bare or with a value. splitFlatArgs pairs a bare
// flag with whatever token follows it, so `open --visible https://x` arrives
// as flags["visible"]="https://x": a value that is not a boolean word is
// handed back as a positional (the URL) and the flag counts as bare true.
func (inv *browserInvocation) applyVisibility(flags map[string]string, bools map[string]bool, positionals []string) []string {
	read := func(key string, bareValue bool) (set, val bool) {
		if bools[key] {
			return true, bareValue
		}
		v, ok := flags[key]
		if !ok {
			return false, false
		}
		if b, isBool := boolWord(v); isBool {
			if !bareValue {
				b = !b
			}
			return true, b
		}
		positionals = append([]string{v}, positionals...)
		return true, bareValue
	}
	for _, key := range []string{"visible", "headed", "show"} {
		if set, val := read(key, true); set {
			inv.visibleSet, inv.visible = true, val
			return positionals
		}
	}
	if set, val := read("headless", false); set {
		inv.visibleSet, inv.visible = true, val
	}
	return positionals
}

// boolWord recognizes the boolean spellings a flattened envelope produces.
func boolWord(v string) (value, isBool bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

// atoiDefault parses n leniently, falling back to def.
func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
