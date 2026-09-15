/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_browser_ext.go — the second-tier @browser verbs (press, hover,
 * select, upload, html, pdf, resize, tabs/tab, cookies, screenshot --full).
 * They live behind an optional backend interface so BrowserBackend — an
 * exported contract — stays byte-identical for anyone implementing it.
 */
package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/pkg/browser"
)

// BrowserExtBackend is the optional surface for the second-tier verbs. The
// real pkg/browser.Session implements it; a backend that does not gets a
// clear "not supported" error instead of a panic.
type BrowserExtBackend interface {
	Press(ctx context.Context, chord string) error
	Hover(ctx context.Context, target string) error
	Select(ctx context.Context, target, option string) error
	Upload(ctx context.Context, target string, paths []string) error
	HTML(ctx context.Context, target string, maxBytes int) (string, error)
	PDF(ctx context.Context, path string) error
	Resize(ctx context.Context, width, height int, mobile bool) error
	ScreenshotFull(ctx context.Context, path string) error
	Tabs(ctx context.Context) ([]browser.TabInfo, error)
	SwitchTab(ctx context.Context, which string) (browser.TabInfo, error)
	Cookies(ctx context.Context, domainFilter string) ([]browser.CookieInfo, error)
	ClearCookies(ctx context.Context) error
}

// Model-facing result strings for the extended verbs.
const (
	browserMsgPressedFmt      = "Pressed %s."
	browserMsgHoveredFmt      = "Hovering %s."
	browserMsgSelectedFmt     = "Selected %q in %s."
	browserMsgUploadedFmt     = "Attached %d file(s) to %s."
	browserMsgPDFFmt          = "PDF saved to %s"
	browserMsgResizedFmt      = "Viewport is now %dx%d (mobile=%t)."
	browserMsgNoTabs          = "No page tabs open."
	browserMsgSwitchedTabFmt  = "Now driving tab: %s (%s)"
	browserMsgNoCookies       = "No cookies in this browser session."
	browserMsgCookiesCleared  = "All cookies cleared — every site is logged out."
	browserMsgExtUnsupported  = "@browser: this backend does not support %q"
	browserMsgSessionCookie   = "session"
	browserMsgCookieHeaderFmt = "%d cookie(s) (values withheld):\n"
	browserMsgTabsHeaderFmt   = "%d tab(s) — `tab N` switches:\n"
)

// browserExt resolves the optional surface or explains why cmd is unavailable.
func browserExt(b BrowserBackend, cmd string) (BrowserExtBackend, error) {
	if ext, ok := b.(BrowserExtBackend); ok {
		return ext, nil
	}
	return nil, fmt.Errorf(browserMsgExtUnsupported, cmd)
}

// browserExtHandlers maps each second-tier verb onto its handler.
var browserExtHandlers = map[string]func(context.Context, BrowserBackend, BrowserExtBackend, browserInvocation) (string, error){
	"press":   browserExtPress,
	"hover":   browserExtHover,
	"select":  browserExtSelect,
	"upload":  browserExtUpload,
	"html":    browserExtHTML,
	"pdf":     browserExtPDF,
	"resize":  browserExtResize,
	"tabs":    browserExtTabs,
	"tab":     browserExtTab,
	"cookies": browserExtCookies,
}

// browserCmdExt dispatches one second-tier verb.
func browserCmdExt(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	ext, err := browserExt(b, inv.cmd)
	if err != nil {
		return "", err
	}
	h, ok := browserExtHandlers[inv.cmd]
	if !ok {
		return "", fmt.Errorf("@browser: unknown cmd %q", inv.cmd)
	}
	return h(ctx, b, ext, inv)
}

// withSnapshot appends the page after an action that may have changed it;
// a failed snapshot never hides the action's success.
func withSnapshot(ctx context.Context, b BrowserBackend, inv browserInvocation, msg string) string {
	time.Sleep(400 * time.Millisecond)
	snap, err := b.Snapshot(ctx, inv.max)
	if err != nil || strings.TrimSpace(snap) == "" {
		return msg
	}
	return msg + "\n\n" + snap
}

func browserExtPress(ctx context.Context, b BrowserBackend, ext BrowserExtBackend, inv browserInvocation) (string, error) {
	if inv.key == "" {
		return "", errors.New(`@browser press: missing key. Example: {"cmd":"press","args":{"key":"Enter"}} (Tab, Escape, ArrowDown, Control+a, …)`)
	}
	if err := ext.Press(ctx, inv.key); err != nil {
		return "", fmt.Errorf("@browser press: %w", err)
	}
	return withSnapshot(ctx, b, inv, fmt.Sprintf(browserMsgPressedFmt, inv.key)), nil
}

func browserExtHover(ctx context.Context, b BrowserBackend, ext BrowserExtBackend, inv browserInvocation) (string, error) {
	if inv.target == "" {
		return "", errors.New(`@browser hover: missing target — a [n] ref from the last snapshot or a CSS selector`)
	}
	if err := ext.Hover(ctx, inv.target); err != nil {
		return "", fmt.Errorf("@browser hover: %w", err)
	}
	return withSnapshot(ctx, b, inv, fmt.Sprintf(browserMsgHoveredFmt, inv.target)), nil
}

func browserExtSelect(ctx context.Context, _ BrowserBackend, ext BrowserExtBackend, inv browserInvocation) (string, error) {
	if inv.target == "" || inv.text == "" {
		return "", errors.New(`@browser select: need target and value. Example: {"cmd":"select","args":{"target":"4","value":"BR"}} (value or visible option text)`)
	}
	if err := ext.Select(ctx, inv.target, inv.text); err != nil {
		return "", fmt.Errorf("@browser select: %w", err)
	}
	return fmt.Sprintf(browserMsgSelectedFmt, inv.text, inv.target), nil
}

func browserExtUpload(ctx context.Context, _ BrowserBackend, ext BrowserExtBackend, inv browserInvocation) (string, error) {
	if inv.target == "" || len(inv.files) == 0 {
		return "", errors.New(`@browser upload: need target and file. Example: {"cmd":"upload","args":{"target":"2","file":"/path/to/report.pdf"}}`)
	}
	if err := ext.Upload(ctx, inv.target, inv.files); err != nil {
		return "", fmt.Errorf("@browser upload: %w", err)
	}
	return fmt.Sprintf(browserMsgUploadedFmt, len(inv.files), inv.target), nil
}

func browserExtHTML(ctx context.Context, _ BrowserBackend, ext BrowserExtBackend, inv browserInvocation) (string, error) {
	out, err := ext.HTML(ctx, inv.target, inv.max)
	if err != nil {
		return "", fmt.Errorf("@browser html: %w", err)
	}
	return out, nil
}

func browserExtPDF(ctx context.Context, _ BrowserBackend, ext BrowserExtBackend, inv browserInvocation) (string, error) {
	path := inv.file
	if path == "" {
		path = filepath.Join(os.TempDir(), "chatcli-browser", fmt.Sprintf("page-%d.pdf", time.Now().UnixMilli()))
	}
	if err := ext.PDF(ctx, path); err != nil {
		return "", fmt.Errorf("@browser pdf: %w", err)
	}
	return fmt.Sprintf(browserMsgPDFFmt, path), nil
}

func browserExtResize(ctx context.Context, _ BrowserBackend, ext BrowserExtBackend, inv browserInvocation) (string, error) {
	if inv.width <= 0 || inv.height <= 0 {
		return "", errors.New(`@browser resize: need width and height. Example: {"cmd":"resize","args":{"width":390,"height":844,"mobile":true}}`)
	}
	if err := ext.Resize(ctx, inv.width, inv.height, inv.mobile); err != nil {
		return "", fmt.Errorf("@browser resize: %w", err)
	}
	return fmt.Sprintf(browserMsgResizedFmt, inv.width, inv.height, inv.mobile), nil
}

func browserExtTabs(ctx context.Context, _ BrowserBackend, ext BrowserExtBackend, _ browserInvocation) (string, error) {
	tabs, err := ext.Tabs(ctx)
	if err != nil {
		return "", fmt.Errorf("@browser tabs: %w", err)
	}
	return renderTabs(tabs), nil
}

func browserExtTab(ctx context.Context, b BrowserBackend, ext BrowserExtBackend, inv browserInvocation) (string, error) {
	if inv.target == "" {
		return "", errors.New(`@browser tab: missing tab number — run tabs first. Example: {"cmd":"tab","args":{"target":"2"}}`)
	}
	tab, err := ext.SwitchTab(ctx, inv.target)
	if err != nil {
		return "", fmt.Errorf("@browser tab: %w", err)
	}
	return withSnapshot(ctx, b, inv, fmt.Sprintf(browserMsgSwitchedTabFmt, tab.Title, tab.URL)), nil
}

func browserExtCookies(ctx context.Context, _ BrowserBackend, ext BrowserExtBackend, inv browserInvocation) (string, error) {
	if inv.clear {
		if err := ext.ClearCookies(ctx); err != nil {
			return "", fmt.Errorf("@browser cookies: %w", err)
		}
		return browserMsgCookiesCleared, nil
	}
	cookies, err := ext.Cookies(ctx, inv.text)
	if err != nil {
		return "", fmt.Errorf("@browser cookies: %w", err)
	}
	return renderCookies(cookies), nil
}

// renderTabs formats the tab list for the model.
func renderTabs(tabs []browser.TabInfo) string {
	if len(tabs) == 0 {
		return browserMsgNoTabs
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, browserMsgTabsHeaderFmt, len(tabs))
	for i, t := range tabs {
		marker := " "
		if t.Current {
			marker = "*"
		}
		fmt.Fprintf(&sb, "%s[%d] %s — %s\n", marker, i+1, strings.TrimSpace(t.Title), t.URL)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderCookies formats the cookie listing (no values) for the model.
func renderCookies(cookies []browser.CookieInfo) string {
	if len(cookies) == 0 {
		return browserMsgNoCookies
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, browserMsgCookieHeaderFmt, len(cookies))
	for _, c := range cookies {
		exp := browserMsgSessionCookie
		if !c.Expires.IsZero() {
			exp = c.Expires.UTC().Format(time.RFC3339)
		}
		flags := make([]string, 0, 2)
		if c.HTTPOnly {
			flags = append(flags, "httpOnly")
		}
		if c.Secure {
			flags = append(flags, "secure")
		}
		fmt.Fprintf(&sb, "%s  domain=%s path=%s expires=%s %s\n", c.Name, c.Domain, c.Path, exp, strings.Join(flags, ","))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// parseDims reads "1280x800" or two positional numbers.
func parseDims(positionals []string) (w, h int, rest []string) {
	if len(positionals) > 0 {
		if x := strings.SplitN(strings.ToLower(positionals[0]), "x", 2); len(x) == 2 {
			if a, err := strconv.Atoi(x[0]); err == nil {
				if b, err := strconv.Atoi(x[1]); err == nil {
					return a, b, positionals[1:]
				}
			}
		}
	}
	if len(positionals) > 1 {
		if a, err := strconv.Atoi(positionals[0]); err == nil {
			if b, err := strconv.Atoi(positionals[1]); err == nil {
				return a, b, positionals[2:]
			}
		}
	}
	return 0, 0, positionals
}
