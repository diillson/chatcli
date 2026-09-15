/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * session.go — the live browser session behind the @browser tool.
 *
 * One Session = one launched browser + one attached page target. Actions are
 * the verbs the tool exposes (navigate, snapshot, click, type, eval,
 * screenshot, scroll, back, console, network). Console messages and network
 * responses are captured continuously into bounded rings so the model can
 * ask "what did the page log?" after the fact — the debugging loop every
 * browser-capable agent CLI converged on.
 *
 * Element addressing: Snapshot stamps every interactive element with a
 * data-chatcli-ref attribute and returns a numbered listing; click/type
 * accept either that ref number or a raw CSS selector. Refs survive until
 * the next navigation.
 */
package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// maxRing bounds the console/network capture rings.
	maxRing = 400
	// navigateWait bounds how long Navigate waits for the load event before
	// returning with whatever the page has (SPAs may never "finish").
	navigateWait = 20 * time.Second
	// defaultSnapshotBytes bounds the page-text portion of a snapshot.
	defaultSnapshotBytes = 12_000
)

// ConsoleEntry is one captured console message.
type ConsoleEntry struct {
	Kind string // log|warn|error|info|debug
	Text string
}

// NetworkEntry is one captured network response.
type NetworkEntry struct {
	Method string
	URL    string
	Status int
	Type   string // Document|XHR|Fetch|Script|…
}

// Mode is the visibility a caller asks for when acquiring the session.
type Mode int

const (
	// ModeDefault keeps a running session as it is; a fresh launch follows
	// CHATCLI_BROWSER_HEADLESS.
	ModeDefault Mode = iota
	// ModeVisible wants a real window on the user's screen — relaunching a
	// headless session on the same profile if needed.
	ModeVisible
	// ModeHeadless wants no window — relaunching a visible session on the
	// same profile if needed.
	ModeHeadless
)

// Session drives one page in one launched browser.
type Session struct {
	cmd         *exec.Cmd
	conn        *cdpConn
	userDataDir string
	ephemeral   bool // profile dir is ours to delete on close
	headless    bool
	attached    bool // the user's own browser (CDPURLEnv): never killed or relaunched
	targetID    string
	sessionID   string

	mu         sync.Mutex
	targetGone bool // the page target was closed (user closed the tab/window)
	console    []ConsoleEntry
	network    []NetworkEntry
	requests   map[string]NetworkEntry // requestId -> method/url awaiting response
	loadCh     chan struct{}           // closed on Page.loadEventFired; replaced per navigation
}

// NewSession launches a browser, attaches to a fresh page target and enables
// the domains the actions need.
func NewSession(ctx context.Context) (*Session, error) {
	if ep := attachURL(); ep != "" {
		return attachSession(ctx, ep)
	}
	return launchSession(ctx, headlessEnabled(), persistentProfileDir())
}

// launchSession launches a browser with the given visibility on userDataDir
// ("" = throwaway profile owned by the session) and attaches to a fresh
// page target.
func launchSession(ctx context.Context, headless bool, userDataDir string) (*Session, error) {
	ephemeral := userDataDir == ""
	cmd, wsURL, dataDir, err := launchChrome(ctx, headless, userDataDir)
	if err != nil {
		return nil, err
	}
	s := &Session{
		cmd:         cmd,
		userDataDir: dataDir,
		ephemeral:   ephemeral,
		headless:    headless,
		requests:    make(map[string]NetworkEntry),
		loadCh:      make(chan struct{}),
	}
	conn, err := dialCDP(ctx, wsURL, s.handleEvent)
	if err != nil {
		s.killBrowser()
		return nil, err
	}
	s.conn = conn

	if err := s.attachFreshTarget(ctx); err != nil {
		s.Close(ctx)
		return nil, err
	}
	return s, nil
}

// ErrPageClosed reports that the page the session drove is gone — the user
// closed the tab or the window. The session itself stays usable: the next
// command attaches a fresh blank tab.
var ErrPageClosed = errors.New("the browser page was closed (tab or window closed by the user); a fresh blank tab is attached on the next command — open or show a URL again")

// cdpSessionNotFound is the DevTools error code for a command sent to a
// session that no longer exists.
const cdpSessionNotFound = -32001

// attachFreshTarget creates an about:blank page target, attaches to it and
// enables Page/Runtime/Network events. Target discovery is switched on so
// the session hears when its page is destroyed.
func (s *Session) attachFreshTarget(ctx context.Context) error {
	_, _ = s.conn.call(ctx, "", "Target.setDiscoverTargets", map[string]interface{}{"discover": true})
	res, err := s.conn.call(ctx, "", "Target.createTarget", map[string]interface{}{"url": "about:blank"})
	if err != nil {
		return err
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(res, &created); err != nil {
		return err
	}

	res, err = s.conn.call(ctx, "", "Target.attachToTarget", map[string]interface{}{
		"targetId": created.TargetID, "flatten": true,
	})
	if err != nil {
		return err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &attached); err != nil {
		return err
	}
	s.mu.Lock()
	s.targetID, s.sessionID = created.TargetID, attached.SessionID
	s.targetGone = false
	s.loadCh = make(chan struct{})
	s.mu.Unlock()

	for _, method := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
		if _, err := s.conn.call(ctx, attached.SessionID, method, nil); err != nil {
			return fmt.Errorf("%s: %w", method, err)
		}
	}
	return nil
}

// call sends a command to the page session. A page the user closed is
// detected two ways — the detach event marks it, and the browser answers
// "session not found" — and surfaces as ErrPageClosed; the NEXT call after
// that attaches a fresh blank tab first, so the tool keeps working.
func (s *Session) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	s.mu.Lock()
	gone, sid := s.targetGone, s.sessionID
	s.mu.Unlock()
	if gone {
		if err := s.attachFreshTarget(ctx); err != nil {
			return nil, fmt.Errorf("reattach after the page was closed: %w", err)
		}
		s.mu.Lock()
		sid = s.sessionID
		s.mu.Unlock()
	}
	res, err := s.conn.call(ctx, sid, method, params)
	var cerr *cdpError
	if err != nil && errors.As(err, &cerr) && cerr.Code == cdpSessionNotFound {
		s.mu.Lock()
		s.targetGone = true
		s.mu.Unlock()
		return nil, ErrPageClosed
	}
	return res, err
}

// PageClosed reports whether the page the session drove has been closed
// and no command has reattached a fresh one yet.
func (s *Session) PageClosed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targetGone
}

// handleEvent routes CDP events into the capture rings and load waiters.
func (s *Session) handleEvent(method string, params json.RawMessage, sessionID string) {
	s.mu.Lock()
	current := s.sessionID
	s.mu.Unlock()
	if sessionID != "" && sessionID != current {
		return
	}
	switch method {
	case "Target.detachedFromTarget", "Target.targetDestroyed":
		var ev struct {
			SessionID string `json:"sessionId"`
			TargetID  string `json:"targetId"`
		}
		_ = json.Unmarshal(params, &ev)
		s.mu.Lock()
		if (ev.SessionID != "" && ev.SessionID == s.sessionID) || (ev.TargetID != "" && ev.TargetID == s.targetID) {
			s.targetGone = true
		}
		s.mu.Unlock()

	case "Page.javascriptDialogOpening":
		// An alert/confirm/prompt blocks the page (and every Runtime.evaluate
		// with it). Accept it right away and leave a trace in the console
		// ring so the model knows the page raised one.
		var ev struct {
			Type          string `json:"type"`
			Message       string `json:"message"`
			DefaultPrompt string `json:"defaultPrompt"`
		}
		_ = json.Unmarshal(params, &ev)
		s.appendConsole(ConsoleEntry{Kind: "dialog", Text: fmt.Sprintf("%s: %s (auto-accepted)", ev.Type, ev.Message)})
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = s.conn.call(ctx, current, "Page.handleJavaScriptDialog", map[string]interface{}{
				"accept": true, "promptText": ev.DefaultPrompt,
			})
		}()

	case "Page.loadEventFired":
		s.mu.Lock()
		select {
		case <-s.loadCh:
			// already closed
		default:
			close(s.loadCh)
		}
		s.mu.Unlock()

	case "Runtime.consoleAPICalled":
		var ev struct {
			Type string `json:"type"`
			Args []struct {
				Value       interface{} `json:"value"`
				Description string      `json:"description"`
			} `json:"args"`
		}
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		parts := make([]string, 0, len(ev.Args))
		for _, a := range ev.Args {
			if a.Value != nil {
				parts = append(parts, fmt.Sprintf("%v", a.Value))
			} else if a.Description != "" {
				parts = append(parts, a.Description)
			}
		}
		s.appendConsole(ConsoleEntry{Kind: ev.Type, Text: strings.Join(parts, " ")})

	case "Runtime.exceptionThrown":
		var ev struct {
			ExceptionDetails struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		text := ev.ExceptionDetails.Exception.Description
		if text == "" {
			text = ev.ExceptionDetails.Text
		}
		s.appendConsole(ConsoleEntry{Kind: "error", Text: text})

	case "Network.requestWillBeSent":
		var ev struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Request   struct {
				Method string `json:"method"`
				URL    string `json:"url"`
			} `json:"request"`
		}
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		s.mu.Lock()
		s.requests[ev.RequestID] = NetworkEntry{Method: ev.Request.Method, URL: ev.Request.URL, Type: ev.Type}
		s.mu.Unlock()

	case "Network.responseReceived":
		var ev struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Response  struct {
				Status int    `json:"status"`
				URL    string `json:"url"`
			} `json:"response"`
		}
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		s.mu.Lock()
		entry, ok := s.requests[ev.RequestID]
		if !ok {
			entry = NetworkEntry{Method: "GET", URL: ev.Response.URL, Type: ev.Type}
		}
		delete(s.requests, ev.RequestID)
		entry.Status = ev.Response.Status
		s.network = append(s.network, entry)
		if len(s.network) > maxRing {
			s.network = s.network[len(s.network)-maxRing:]
		}
		s.mu.Unlock()
	}
}

func (s *Session) appendConsole(e ConsoleEntry) {
	s.mu.Lock()
	s.console = append(s.console, e)
	if len(s.console) > maxRing {
		s.console = s.console[len(s.console)-maxRing:]
	}
	s.mu.Unlock()
}

// Navigate loads url and waits for the load event (bounded — a page that
// never fires load still returns with its current state).
func (s *Session) Navigate(ctx context.Context, url string) (title, finalURL string, err error) {
	s.mu.Lock()
	s.loadCh = make(chan struct{})
	loadCh := s.loadCh
	s.mu.Unlock()

	if _, err := s.call(ctx, "Page.navigate", map[string]interface{}{"url": url}); err != nil {
		return "", "", err
	}
	timer := time.NewTimer(navigateWait)
	defer timer.Stop()
	select {
	case <-loadCh:
	case <-timer.C:
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	return s.pageIdentity(ctx)
}

// Back goes one step back in history and reports the new identity.
func (s *Session) Back(ctx context.Context) (title, finalURL string, err error) {
	_, err = s.Eval(ctx, "history.back()")
	if err != nil {
		return "", "", err
	}
	time.Sleep(700 * time.Millisecond) // history navigation has no reliable single event
	return s.pageIdentity(ctx)
}

// Identity returns the current document title and URL.
func (s *Session) Identity(ctx context.Context) (title, url string, err error) {
	return s.pageIdentity(ctx)
}

// pageIdentity returns the current document title and URL.
func (s *Session) pageIdentity(ctx context.Context) (string, string, error) {
	raw, err := s.evalRaw(ctx, `JSON.stringify({t: document.title, u: location.href})`)
	if err != nil {
		return "", "", err
	}
	var id struct {
		T string `json:"t"`
		U string `json:"u"`
	}
	if err := json.Unmarshal([]byte(raw), &id); err != nil {
		return "", "", err
	}
	return id.T, id.U, nil
}

// snapshotJS stamps interactive elements with data-chatcli-ref and returns
// the page identity, a numbered interactive-element listing and the visible
// text. Kept as one expression so a single Runtime.evaluate does the job.
const snapshotJS = `(() => {
  const interactiveSel = 'a[href], button, input, select, textarea, [role="button"], [onclick]';
  const els = Array.from(document.querySelectorAll(interactiveSel));
  const vis = els.filter(el => {
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  });
  const items = vis.slice(0, 150).map((el, i) => {
    el.setAttribute('data-chatcli-ref', String(i + 1));
    const tag = el.tagName.toLowerCase();
    let label = (el.innerText || el.value || el.getAttribute('aria-label') || el.getAttribute('placeholder') || el.getAttribute('name') || '').trim().replace(/\s+/g, ' ').slice(0, 80);
    let extra = '';
    if (tag === 'a') extra = ' -> ' + (el.getAttribute('href') || '').slice(0, 120);
    if (tag === 'input') extra = ' [type=' + (el.type || 'text') + ']';
    return '[' + (i + 1) + '] <' + tag + '> ' + label + extra;
  });
  return JSON.stringify({
    title: document.title,
    url: location.href,
    interactive: items,
    text: (document.body ? document.body.innerText : '').replace(/\n{3,}/g, '\n\n')
  });
})()`

// Snapshot renders the current page as model-facing text: identity, numbered
// interactive elements (click/type targets) and visible text capped at
// maxBytes (0 = default).
func (s *Session) Snapshot(ctx context.Context, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = defaultSnapshotBytes
	}
	raw, err := s.evalRaw(ctx, snapshotJS)
	if err != nil {
		return "", err
	}
	var snap struct {
		Title       string   `json:"title"`
		URL         string   `json:"url"`
		Interactive []string `json:"interactive"`
		Text        string   `json:"text"`
	}
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return "", fmt.Errorf("parse snapshot: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Page: %s\nURL: %s\n", snap.Title, snap.URL)
	if len(snap.Interactive) > 0 {
		b.WriteString("\nInteractive elements (use the [n] ref with click/type):\n")
		for _, it := range snap.Interactive {
			b.WriteString(it)
			b.WriteString("\n")
		}
	}
	text := strings.TrimSpace(snap.Text)
	if len(text) > maxBytes {
		text = text[:maxBytes] + "\n… (page text truncated)"
	}
	if text != "" {
		b.WriteString("\nPage text:\n")
		b.WriteString(text)
	}
	return b.String(), nil
}

// resolveSelector turns a ref number from the last snapshot into its stamped
// selector; anything else passes through as a CSS selector.
func resolveSelector(target string) string {
	t := strings.TrimSpace(target)
	trimmed := strings.Trim(t, "[]")
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return t
		}
	}
	if trimmed == "" {
		return t
	}
	return fmt.Sprintf(`[data-chatcli-ref="%s"]`, trimmed)
}

// Click clicks the element addressed by a snapshot ref or CSS selector.
func (s *Session) Click(ctx context.Context, target string) error {
	sel := resolveSelector(target)
	selJSON, _ := json.Marshal(sel)
	js := fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return 'NOTFOUND';
  el.scrollIntoView({block: 'center'});
  el.click();
  return 'OK';
})()`, selJSON)
	out, err := s.evalRaw(ctx, js)
	if err != nil {
		return err
	}
	if strings.Contains(out, "NOTFOUND") {
		return fmt.Errorf("no element matches %q — run snapshot first and use one of its [n] refs", target)
	}
	return nil
}

// Type focuses the element, sets its value with proper input events, and
// optionally submits the enclosing form.
func (s *Session) Type(ctx context.Context, target, text string, submit bool) error {
	sel := resolveSelector(target)
	selJSON, _ := json.Marshal(sel)
	textJSON, _ := json.Marshal(text)
	js := fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return 'NOTFOUND';
  el.scrollIntoView({block: 'center'});
  el.focus();
  const setter = Object.getOwnPropertyDescriptor(el.__proto__, 'value');
  if (setter && setter.set) { setter.set.call(el, %s); } else { el.value = %s; }
  el.dispatchEvent(new Event('input', {bubbles: true}));
  el.dispatchEvent(new Event('change', {bubbles: true}));
  if (%t) {
    el.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', keyCode: 13, bubbles: true}));
    if (el.form) { el.form.requestSubmit ? el.form.requestSubmit() : el.form.submit(); }
  }
  return 'OK';
})()`, selJSON, textJSON, textJSON, submit)
	out, err := s.evalRaw(ctx, js)
	if err != nil {
		return err
	}
	if strings.Contains(out, "NOTFOUND") {
		return fmt.Errorf("no element matches %q — run snapshot first and use one of its [n] refs", target)
	}
	return nil
}

// Scroll moves the viewport: direction up|down|top|bottom, or into view of a
// ref/selector when target is non-empty.
func (s *Session) Scroll(ctx context.Context, direction, target string) error {
	var js string
	if strings.TrimSpace(target) != "" {
		selJSON, _ := json.Marshal(resolveSelector(target))
		js = fmt.Sprintf(`(() => { const el = document.querySelector(%s); if (!el) return 'NOTFOUND'; el.scrollIntoView({block:'center'}); return 'OK'; })()`, selJSON)
	} else {
		switch direction {
		case "up":
			js = `(() => { window.scrollBy(0, -window.innerHeight * 0.8); return 'OK'; })()`
		case "top":
			js = `(() => { window.scrollTo(0, 0); return 'OK'; })()`
		case "bottom":
			js = `(() => { window.scrollTo(0, document.body.scrollHeight); return 'OK'; })()`
		default: // down
			js = `(() => { window.scrollBy(0, window.innerHeight * 0.8); return 'OK'; })()`
		}
	}
	out, err := s.evalRaw(ctx, js)
	if err != nil {
		return err
	}
	if strings.Contains(out, "NOTFOUND") {
		return fmt.Errorf("no element matches %q", target)
	}
	return nil
}

// Eval runs a JavaScript expression in the page and returns its value
// rendered as text (JSON for objects).
func (s *Session) Eval(ctx context.Context, expression string) (string, error) {
	return s.evalRaw(ctx, expression)
}

// evalRaw is Runtime.evaluate with returnByValue: results come back as JSON
// text (strings verbatim, objects marshaled).
func (s *Session) evalRaw(ctx context.Context, expression string) (string, error) {
	res, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		Result struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	if out.ExceptionDetails != nil {
		msg := out.ExceptionDetails.Exception.Description
		if msg == "" {
			msg = out.ExceptionDetails.Text
		}
		return "", fmt.Errorf("page JavaScript error: %s", msg)
	}
	if out.Result.Type == "string" {
		var sv string
		if json.Unmarshal(out.Result.Value, &sv) == nil {
			return sv, nil
		}
	}
	if len(out.Result.Value) > 0 {
		return string(out.Result.Value), nil
	}
	return out.Result.Description, nil
}

// Screenshot captures the viewport as PNG into path (directories created).
func (s *Session) Screenshot(ctx context.Context, path string) error {
	return s.captureScreenshot(ctx, path, map[string]interface{}{"format": "png"})
}

// captureScreenshot runs Page.captureScreenshot with params and writes the
// PNG to path.
func (s *Session) captureScreenshot(ctx context.Context, path string, params map[string]interface{}) error {
	res, err := s.call(ctx, "Page.captureScreenshot", params)
	if err != nil {
		return err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return err
	}
	png, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return fmt.Errorf("decode screenshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, png, 0o600)
}

// ConsoleTail returns the last n captured console messages.
func (s *Session) ConsoleTail(n int) []ConsoleEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.console) {
		n = len(s.console)
	}
	out := make([]ConsoleEntry, n)
	copy(out, s.console[len(s.console)-n:])
	return out
}

// NetworkTail returns the last n captured network responses.
func (s *Session) NetworkTail(n int) []NetworkEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 || n > len(s.network) {
		n = len(s.network)
	}
	out := make([]NetworkEntry, n)
	copy(out, s.network[len(s.network)-n:])
	return out
}

// Visible reports whether the session runs with a window on screen.
func (s *Session) Visible() bool { return s != nil && !s.headless }

// Attached reports whether the session drives the user's own browser
// (CDPURLEnv) rather than one ChatCLI launched.
func (s *Session) Attached() bool { return s != nil && s.attached }

// ProfileDir is the user-data directory the session runs on.
func (s *Session) ProfileDir() string {
	if s == nil {
		return ""
	}
	return s.userDataDir
}

// Close tears down the connection, the browser process and (when the
// profile is a throwaway) its directory. Idempotent; ctx bounds the polite
// Browser.close attempt, which is given a moment to flush cookies and
// storage to disk before the process is killed — a persistent or reused
// profile must not lose the login the user just performed.
func (s *Session) Close(ctx context.Context) {
	if s.attached {
		s.detach(ctx)
		return
	}
	if s.conn != nil {
		closeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, closeErr := s.conn.call(closeCtx, "", "Browser.close", nil)
		cancel()
		s.conn.close()
		if closeErr == nil {
			s.awaitExit(gracefulExitWait)
		}
	}
	s.killBrowser()
}

// detach closes only the tab the session opened in the user's browser and
// drops the connection; the browser itself is theirs and stays up.
func (s *Session) detach(ctx context.Context) {
	if s.conn == nil {
		return
	}
	if s.targetID != "" {
		closeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, _ = s.conn.call(closeCtx, "", "Target.closeTarget", map[string]interface{}{"targetId": s.targetID})
		cancel()
	}
	s.conn.close()
}

// gracefulExitWait bounds how long Close lets the browser exit on its own
// after Browser.close before killing it.
const gracefulExitWait = 5 * time.Second

// awaitExit waits up to d for the browser process to exit by itself.
func (s *Session) awaitExit(d time.Duration) {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = s.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

func (s *Session) killBrowser() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	if s.userDataDir != "" && s.ephemeral {
		_ = os.RemoveAll(s.userDataDir)
	}
}

// Alive reports whether the session's connection is still usable.
func (s *Session) Alive() bool {
	if s == nil || s.conn == nil {
		return false
	}
	select {
	case <-s.conn.closed:
		return false
	default:
		return true
	}
}

// --- default session management -------------------------------------------

var (
	defaultMu      sync.Mutex
	defaultSession *Session
)

// Acquire returns the process-wide session, launching the browser on first
// use or after a crash/close.
func Acquire(ctx context.Context) (*Session, error) {
	return AcquireMode(ctx, ModeDefault)
}

// AcquireMode returns the process-wide session in the requested visibility.
// A running session whose visibility differs is relaunched on the SAME
// profile directory and brought back to the page it was on, so cookies and
// logins survive the flip: the agent can surface the window for the user to
// sign in, then keep driving the authenticated session headless.
func AcquireMode(ctx context.Context, mode Mode) (*Session, error) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultSession.Alive() {
		if mode == ModeDefault || defaultSession.attached || defaultSession.headless == (mode == ModeHeadless) {
			return defaultSession, nil
		}
		s, err := relaunch(ctx, defaultSession, mode == ModeHeadless)
		if err != nil {
			return nil, err
		}
		defaultSession = s
		return s, nil
	}
	if defaultSession != nil {
		defaultSession.Close(ctx)
	}
	var s *Session
	var err error
	if ep := attachURL(); ep != "" {
		s, err = attachSession(ctx, ep)
	} else {
		headless := headlessEnabled()
		switch mode {
		case ModeVisible:
			headless = false
		case ModeHeadless:
			headless = true
		}
		s, err = launchSession(ctx, headless, persistentProfileDir())
	}
	if err != nil {
		return nil, err
	}
	defaultSession = s
	return s, nil
}

// relaunch closes prev and starts a new session with the given visibility on
// prev's profile directory, then restores the page prev was showing. On
// launch failure the profile is kept (a throwaway one is adopted by nobody,
// so it is removed) and the error returned; prev is closed either way.
func relaunch(ctx context.Context, prev *Session, headless bool) (*Session, error) {
	_, prevURL, _ := prev.Identity(ctx)
	dir, ephemeral := prev.userDataDir, prev.ephemeral
	prev.ephemeral = false // the new session inherits the directory
	prev.Close(ctx)

	s, err := launchSession(ctx, headless, dir)
	if err != nil {
		if ephemeral {
			_ = os.RemoveAll(dir)
		}
		return nil, fmt.Errorf("relaunch browser: %w", err)
	}
	s.ephemeral = ephemeral
	if prevURL != "" && prevURL != "about:blank" {
		if _, _, err := s.Navigate(ctx, prevURL); err != nil {
			// The session is usable even if the old page will not load again.
			s.appendConsole(ConsoleEntry{Kind: "warn", Text: "chatcli: could not restore " + prevURL + " after relaunch: " + err.Error()})
		}
	}
	return s, nil
}

// DefaultAlive reports whether a process-wide session is running. Never
// launches a browser.
func DefaultAlive() bool {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultSession.Alive()
}

// DefaultVisible reports whether the process-wide session is alive and
// showing a window. Never launches a browser.
func DefaultVisible() bool {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultSession.Alive() && defaultSession.Visible()
}

// DefaultAttached reports whether the process-wide session drives the
// user's own browser. Never launches a browser.
func DefaultAttached() bool {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultSession.Alive() && defaultSession.Attached()
}

// DefaultProfile reports the profile directory the process-wide session
// runs on and whether it persists across ChatCLI runs ("" when no session
// or when attached to the user's browser).
func DefaultProfile() (dir string, persistent bool) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if !defaultSession.Alive() || defaultSession.attached {
		return "", false
	}
	return defaultSession.userDataDir, !defaultSession.ephemeral
}

// DefaultStatus reports whether the process-wide session is alive and, when
// it is, the page it sits on. Never launches a browser.
func DefaultStatus(ctx context.Context) (running bool, title, url string) {
	defaultMu.Lock()
	s := defaultSession
	defaultMu.Unlock()
	if !s.Alive() {
		return false, "", ""
	}
	if s.PageClosed() {
		// Never reattach from a status probe; the next real command does.
		return true, "", ""
	}
	title, url, err := s.Identity(ctx)
	if err != nil {
		return true, "", ""
	}
	return true, title, url
}

// DefaultPageClosed reports whether the process-wide session's page was
// closed by the user and not yet replaced. Never launches a browser.
func DefaultPageClosed() bool {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	return defaultSession.Alive() && defaultSession.PageClosed()
}

// Shutdown closes the process-wide session if one is running. Wired into the
// CLI teardown so a headless browser never outlives ChatCLI.
func Shutdown(ctx context.Context) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultSession != nil {
		defaultSession.Close(ctx)
		defaultSession = nil
	}
}
