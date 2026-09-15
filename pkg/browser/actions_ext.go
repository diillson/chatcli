/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * actions_ext.go — the second tier of @browser verbs: keyboard input,
 * hover, select options, file upload, HTML dumps, PDF export, viewport
 * emulation, tab management and cookie inspection. Everything a login or a
 * real UI verification needs beyond click/type, still over the same small
 * CDP client.
 */
package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TabInfo describes one page target in the browser.
type TabInfo struct {
	ID      string
	Title   string
	URL     string
	Current bool
}

// CookieInfo is one cookie without its value — enough to tell whether a
// login stuck, never enough to leak a session token into the transcript.
type CookieInfo struct {
	Name     string
	Domain   string
	Path     string
	Expires  time.Time // zero for session cookies
	HTTPOnly bool
	Secure   bool
}

// keyDef is the CDP key description for one named key.
type keyDef struct {
	key, code string
	vk        int
	text      string
}

// keyDefs maps the key names the model is likely to use onto CDP events.
var keyDefs = map[string]keyDef{
	"enter":      {"Enter", "Enter", 13, "\r"},
	"return":     {"Enter", "Enter", 13, "\r"},
	"tab":        {"Tab", "Tab", 9, ""},
	"escape":     {"Escape", "Escape", 27, ""},
	"esc":        {"Escape", "Escape", 27, ""},
	"backspace":  {"Backspace", "Backspace", 8, ""},
	"delete":     {"Delete", "Delete", 46, ""},
	"space":      {" ", "Space", 32, " "},
	"arrowup":    {"ArrowUp", "ArrowUp", 38, ""},
	"up":         {"ArrowUp", "ArrowUp", 38, ""},
	"arrowdown":  {"ArrowDown", "ArrowDown", 40, ""},
	"down":       {"ArrowDown", "ArrowDown", 40, ""},
	"arrowleft":  {"ArrowLeft", "ArrowLeft", 37, ""},
	"left":       {"ArrowLeft", "ArrowLeft", 37, ""},
	"arrowright": {"ArrowRight", "ArrowRight", 39, ""},
	"right":      {"ArrowRight", "ArrowRight", 39, ""},
	"home":       {"Home", "Home", 36, ""},
	"end":        {"End", "End", 35, ""},
	"pageup":     {"PageUp", "PageUp", 33, ""},
	"pagedown":   {"PageDown", "PageDown", 34, ""},
	"f5":         {"F5", "F5", 116, ""},
}

// modifierBits are CDP Input modifier flags.
var modifierBits = map[string]int{
	"alt": 1, "option": 1,
	"ctrl": 2, "control": 2,
	"meta": 4, "cmd": 4, "command": 4, "super": 4, "win": 4,
	"shift": 8,
}

// Press sends a key chord to the focused element: "Enter", "Tab", "Escape",
// "ArrowDown", a single printable character, or a chord like "Control+a"
// / "Meta+Enter".
func (s *Session) Press(ctx context.Context, chord string) error {
	parts := strings.Split(strings.TrimSpace(chord), "+")
	if len(parts) == 0 || parts[len(parts)-1] == "" {
		return fmt.Errorf("press: empty key")
	}
	name := parts[len(parts)-1]
	modifiers := 0
	for _, m := range parts[:len(parts)-1] {
		bit, ok := modifierBits[strings.ToLower(strings.TrimSpace(m))]
		if !ok {
			return fmt.Errorf("press: unknown modifier %q (use Control, Alt, Shift, Meta)", m)
		}
		modifiers |= bit
	}
	def, ok := keyDefs[strings.ToLower(name)]
	if !ok {
		r := []rune(name)
		if len(r) != 1 {
			return fmt.Errorf("press: unknown key %q (named keys: Enter, Tab, Escape, Backspace, Delete, Space, ArrowUp/Down/Left/Right, Home, End, PageUp, PageDown; or one character)", name)
		}
		def = keyDef{key: name, code: "Key" + strings.ToUpper(name), vk: int(strings.ToUpper(name)[0]), text: name}
		if modifiers&^8 != 0 { // ctrl/alt/meta chords carry no text
			def.text = ""
		}
	}
	down := map[string]interface{}{
		"type": "keyDown", "key": def.key, "code": def.code,
		"windowsVirtualKeyCode": def.vk, "nativeVirtualKeyCode": def.vk, "modifiers": modifiers,
	}
	if def.text != "" {
		down["text"] = def.text
	}
	if _, err := s.conn.call(ctx, s.sessionID, "Input.dispatchKeyEvent", down); err != nil {
		return err
	}
	up := map[string]interface{}{
		"type": "keyUp", "key": def.key, "code": def.code,
		"windowsVirtualKeyCode": def.vk, "nativeVirtualKeyCode": def.vk, "modifiers": modifiers,
	}
	_, err := s.conn.call(ctx, s.sessionID, "Input.dispatchKeyEvent", up)
	return err
}

// elementCenter scrolls the target into view and returns its viewport
// center, for real mouse events.
func (s *Session) elementCenter(ctx context.Context, target string) (x, y float64, err error) {
	selJSON, _ := json.Marshal(resolveSelector(target))
	js := fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return 'NOTFOUND';
  el.scrollIntoView({block: 'center'});
  const r = el.getBoundingClientRect();
  return JSON.stringify({x: r.left + r.width / 2, y: r.top + r.height / 2});
})()`, selJSON)
	out, err := s.evalRaw(ctx, js)
	if err != nil {
		return 0, 0, err
	}
	if strings.Contains(out, "NOTFOUND") {
		return 0, 0, fmt.Errorf("no element matches %q — run snapshot first and use one of its [n] refs", target)
	}
	var pt struct{ X, Y float64 }
	if err := json.Unmarshal([]byte(out), &pt); err != nil {
		return 0, 0, err
	}
	return pt.X, pt.Y, nil
}

// Hover moves the mouse over the element so hover menus and tooltips open.
func (s *Session) Hover(ctx context.Context, target string) error {
	x, y, err := s.elementCenter(ctx, target)
	if err != nil {
		return err
	}
	_, err = s.conn.call(ctx, s.sessionID, "Input.dispatchMouseEvent", map[string]interface{}{
		"type": "mouseMoved", "x": x, "y": y,
	})
	return err
}

// Select chooses an option of a <select> by value or visible text
// (case-insensitive) and fires the input/change events frameworks listen to.
func (s *Session) Select(ctx context.Context, target, option string) error {
	selJSON, _ := json.Marshal(resolveSelector(target))
	optJSON, _ := json.Marshal(option)
	js := fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return 'NOTFOUND';
  if (el.tagName.toLowerCase() !== 'select') return 'NOTSELECT';
  const want = %s.trim().toLowerCase();
  const opts = Array.from(el.options);
  let idx = opts.findIndex(o => o.value.toLowerCase() === want);
  if (idx < 0) idx = opts.findIndex(o => o.text.trim().toLowerCase() === want);
  if (idx < 0) idx = opts.findIndex(o => o.text.trim().toLowerCase().includes(want));
  if (idx < 0) return 'NOOPTION:' + opts.map(o => o.value + '=' + o.text.trim()).join(' | ');
  el.scrollIntoView({block: 'center'});
  el.selectedIndex = idx;
  el.dispatchEvent(new Event('input', {bubbles: true}));
  el.dispatchEvent(new Event('change', {bubbles: true}));
  return 'OK:' + opts[idx].value;
})()`, selJSON, optJSON)
	out, err := s.evalRaw(ctx, js)
	if err != nil {
		return err
	}
	switch {
	case strings.HasPrefix(out, "NOTFOUND"):
		return fmt.Errorf("no element matches %q — run snapshot first and use one of its [n] refs", target)
	case strings.HasPrefix(out, "NOTSELECT"):
		return fmt.Errorf("%q is not a <select>; use type for inputs or click for custom dropdowns", target)
	case strings.HasPrefix(out, "NOOPTION:"):
		return fmt.Errorf("no option matches %q; available: %s", option, strings.TrimPrefix(out, "NOOPTION:"))
	}
	return nil
}

// Upload sets the files of an <input type=file> — the only way to feed a
// file to a page, since the OS picker never opens under automation.
func (s *Session) Upload(ctx context.Context, target string, paths []string) error {
	abs := make([]string, 0, len(paths))
	for _, p := range paths {
		a, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if _, err := os.Stat(a); err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		abs = append(abs, a)
	}
	nodeID, err := s.nodeIDFor(ctx, target)
	if err != nil {
		return err
	}
	_, err = s.conn.call(ctx, s.sessionID, "DOM.setFileInputFiles", map[string]interface{}{
		"files": abs, "nodeId": nodeID,
	})
	return err
}

// nodeIDFor resolves a ref/selector to a DOM node id.
func (s *Session) nodeIDFor(ctx context.Context, target string) (int, error) {
	res, err := s.conn.call(ctx, s.sessionID, "DOM.getDocument", map[string]interface{}{"depth": 0})
	if err != nil {
		return 0, err
	}
	var doc struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(res, &doc); err != nil {
		return 0, err
	}
	res, err = s.conn.call(ctx, s.sessionID, "DOM.querySelector", map[string]interface{}{
		"nodeId": doc.Root.NodeID, "selector": resolveSelector(target),
	})
	if err != nil {
		return 0, err
	}
	var q struct {
		NodeID int `json:"nodeId"`
	}
	if err := json.Unmarshal(res, &q); err != nil {
		return 0, err
	}
	if q.NodeID == 0 {
		return 0, fmt.Errorf("no element matches %q — run snapshot first and use one of its [n] refs", target)
	}
	return q.NodeID, nil
}

// HTML returns the outerHTML of the element (or the whole document when
// target is empty), capped at maxBytes (0 = 20000).
func (s *Session) HTML(ctx context.Context, target string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = 20_000
	}
	var js string
	if strings.TrimSpace(target) == "" {
		js = `document.documentElement.outerHTML`
	} else {
		selJSON, _ := json.Marshal(resolveSelector(target))
		js = fmt.Sprintf(`(() => { const el = document.querySelector(%s); return el ? el.outerHTML : 'NOTFOUND'; })()`, selJSON)
	}
	out, err := s.evalRaw(ctx, js)
	if err != nil {
		return "", err
	}
	if out == "NOTFOUND" {
		return "", fmt.Errorf("no element matches %q — run snapshot first and use one of its [n] refs", target)
	}
	if len(out) > maxBytes {
		out = out[:maxBytes] + "\n… (html truncated)"
	}
	return out, nil
}

// PDF renders the page to a PDF file. Chrome only implements printToPDF
// headless; a visible session gets a clear hint.
func (s *Session) PDF(ctx context.Context, path string) error {
	res, err := s.conn.call(ctx, s.sessionID, "Page.printToPDF", map[string]interface{}{
		"printBackground": true, "preferCSSPageSize": true,
	})
	if err != nil {
		if s.Visible() {
			return fmt.Errorf("%w (PDF export needs a headless session — run hide first)", err)
		}
		return err
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return err
	}
	data, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return fmt.Errorf("decode pdf: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Resize emulates a viewport of width×height CSS pixels; mobile also turns
// on touch/mobile UA hints so responsive layouts switch.
func (s *Session) Resize(ctx context.Context, width, height int, mobile bool) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("resize: width and height must be positive")
	}
	scale := 1
	if mobile {
		scale = 2
	}
	_, err := s.conn.call(ctx, s.sessionID, "Emulation.setDeviceMetricsOverride", map[string]interface{}{
		"width": width, "height": height, "deviceScaleFactor": scale, "mobile": mobile,
	})
	if err != nil {
		return err
	}
	_, err = s.conn.call(ctx, s.sessionID, "Emulation.setTouchEmulationEnabled", map[string]interface{}{"enabled": mobile})
	return err
}

// ScreenshotFull captures the whole scrollable page (not just the viewport)
// as PNG into path.
func (s *Session) ScreenshotFull(ctx context.Context, path string) error {
	res, err := s.conn.call(ctx, s.sessionID, "Page.getLayoutMetrics", nil)
	if err != nil {
		return err
	}
	var m struct {
		CSSContentSize struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"cssContentSize"`
	}
	if err := json.Unmarshal(res, &m); err != nil {
		return err
	}
	params := map[string]interface{}{"format": "png", "captureBeyondViewport": true}
	if m.CSSContentSize.Width > 0 && m.CSSContentSize.Height > 0 {
		params["clip"] = map[string]interface{}{
			"x": 0, "y": 0, "width": m.CSSContentSize.Width, "height": m.CSSContentSize.Height, "scale": 1,
		}
	}
	return s.captureScreenshot(ctx, path, params)
}

// Tabs lists the page targets in the browser, marking the one the session
// drives. Popups (OAuth windows, target=_blank links) show up here.
func (s *Session) Tabs(ctx context.Context) ([]TabInfo, error) {
	res, err := s.conn.call(ctx, "", "Target.getTargets", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	tabs := make([]TabInfo, 0, len(out.TargetInfos))
	for _, t := range out.TargetInfos {
		if t.Type != "page" {
			continue
		}
		tabs = append(tabs, TabInfo{ID: t.TargetID, Title: t.Title, URL: t.URL, Current: t.TargetID == s.targetID})
	}
	return tabs, nil
}

// SwitchTab attaches the session to another page target, addressed by its
// 1-based index in Tabs or by target id, and brings it to the front.
func (s *Session) SwitchTab(ctx context.Context, which string) (TabInfo, error) {
	tabs, err := s.Tabs(ctx)
	if err != nil {
		return TabInfo{}, err
	}
	var pick *TabInfo
	if n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(which), "[]")); err == nil {
		if n < 1 || n > len(tabs) {
			return TabInfo{}, fmt.Errorf("tab %d out of range (1..%d)", n, len(tabs))
		}
		pick = &tabs[n-1]
	} else {
		for i := range tabs {
			if tabs[i].ID == which {
				pick = &tabs[i]
				break
			}
		}
		if pick == nil {
			return TabInfo{}, fmt.Errorf("no tab with id %q — run tabs first", which)
		}
	}
	if pick.Current {
		return *pick, nil
	}
	res, err := s.conn.call(ctx, "", "Target.attachToTarget", map[string]interface{}{
		"targetId": pick.ID, "flatten": true,
	})
	if err != nil {
		return TabInfo{}, err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &attached); err != nil {
		return TabInfo{}, err
	}
	for _, method := range []string{"Page.enable", "Runtime.enable", "Network.enable"} {
		if _, err := s.conn.call(ctx, attached.SessionID, method, nil); err != nil {
			return TabInfo{}, fmt.Errorf("%s: %w", method, err)
		}
	}
	s.mu.Lock()
	s.targetID, s.sessionID = pick.ID, attached.SessionID
	s.loadCh = make(chan struct{})
	s.mu.Unlock()
	_, _ = s.conn.call(ctx, "", "Target.activateTarget", map[string]interface{}{"targetId": pick.ID})
	pick.Current = true
	return *pick, nil
}

// Cookies lists the browser's cookies (names, domains, expiry — never
// values), optionally filtered to those whose domain contains domainFilter.
func (s *Session) Cookies(ctx context.Context, domainFilter string) ([]CookieInfo, error) {
	res, err := s.conn.call(ctx, "", "Storage.getCookies", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Cookies []struct {
			Name     string  `json:"name"`
			Domain   string  `json:"domain"`
			Path     string  `json:"path"`
			Expires  float64 `json:"expires"`
			HTTPOnly bool    `json:"httpOnly"`
			Secure   bool    `json:"secure"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, err
	}
	filter := strings.ToLower(strings.TrimSpace(domainFilter))
	cookies := make([]CookieInfo, 0, len(out.Cookies))
	for _, c := range out.Cookies {
		if filter != "" && !strings.Contains(strings.ToLower(c.Domain), filter) {
			continue
		}
		ci := CookieInfo{Name: c.Name, Domain: c.Domain, Path: c.Path, HTTPOnly: c.HTTPOnly, Secure: c.Secure}
		if c.Expires > 0 {
			ci.Expires = time.Unix(int64(c.Expires), 0)
		}
		cookies = append(cookies, ci)
	}
	return cookies, nil
}

// ClearCookies wipes every cookie in the browser — a logout of everything.
func (s *Session) ClearCookies(ctx context.Context) error {
	_, err := s.conn.call(ctx, "", "Storage.clearCookies", nil)
	return err
}
