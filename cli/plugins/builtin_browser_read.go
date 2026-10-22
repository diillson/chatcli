/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_browser_read.go — the reading and interaction-detail verbs of
 * @browser (text, find, clear, forward, reload, downloads, network --body)
 * and the richer click/type/snapshot forms. They live behind a third
 * optional backend interface so BrowserBackend and BrowserExtBackend —
 * exported contracts — stay byte-identical for anyone implementing them.
 */
package plugins

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/pkg/browser"
)

// BrowserReadBackend is the optional surface for paged reading, page
// search, real-mouse clicks, editor-aware typing, bot-challenge and network
// probes, response bodies and downloads. The real pkg/browser.Session
// implements it; a backend that does not gets the first-tier behavior or
// a clear "not supported" error.
type BrowserReadBackend interface {
	SnapshotAt(ctx context.Context, maxBytes, offset int) (string, error)
	Text(ctx context.Context, target string, offset, maxBytes int) (string, error)
	Find(ctx context.Context, query string, maxMatches int) (string, error)
	ClickWith(ctx context.Context, target string, opts browser.ClickOptions) (note string, err error)
	TypeWith(ctx context.Context, target, text string, appendText, submit bool) (password bool, err error)
	Clear(ctx context.Context, target string) error
	Forward(ctx context.Context) (title, url string, err error)
	Reload(ctx context.Context) (title, url string, err error)
	Challenge(ctx context.Context) (string, error)
	NetworkQuiet(d time.Duration) bool
	ResponseBody(ctx context.Context, filter string, maxBytes int) (browser.NetworkEntry, string, error)
	Downloads() []browser.DownloadInfo
}

// browserRead resolves the optional surface or explains why cmd is unavailable.
func browserRead(b BrowserBackend, cmd string) (BrowserReadBackend, error) {
	if r, ok := b.(BrowserReadBackend); ok {
		return r, nil
	}
	return nil, fmt.Errorf(browserMsgExtUnsupported, cmd)
}

// browserReadHandlers maps each reading verb onto its handler.
var browserReadHandlers = map[string]func(context.Context, BrowserBackend, BrowserReadBackend, browserInvocation) (string, error){
	"text":      browserReadText,
	"find":      browserReadFind,
	"clear":     browserReadClear,
	"forward":   browserReadForward,
	"reload":    browserReadReload,
	"downloads": browserReadDownloads,
}

// browserCmdRead dispatches one reading verb.
func browserCmdRead(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	r, err := browserRead(b, inv.cmd)
	if err != nil {
		return "", err
	}
	h, ok := browserReadHandlers[inv.cmd]
	if !ok {
		return "", fmt.Errorf("@browser: unknown cmd %q", inv.cmd)
	}
	return h(ctx, b, r, inv)
}

func browserReadText(ctx context.Context, _ BrowserBackend, r BrowserReadBackend, inv browserInvocation) (string, error) {
	out, err := r.Text(ctx, inv.target, inv.offset, inv.max)
	if err != nil {
		return "", fmt.Errorf("@browser text: %w", err)
	}
	return out, nil
}

func browserReadFind(ctx context.Context, _ BrowserBackend, r BrowserReadBackend, inv browserInvocation) (string, error) {
	if strings.TrimSpace(inv.text) == "" {
		return "", errors.New(`@browser find: missing text. Example: {"cmd":"find","args":{"text":"Sign in"}}`)
	}
	out, err := r.Find(ctx, inv.text, inv.max)
	if err != nil {
		return "", fmt.Errorf("@browser find: %w", err)
	}
	return out, nil
}

func browserReadClear(ctx context.Context, _ BrowserBackend, r BrowserReadBackend, inv browserInvocation) (string, error) {
	if inv.target == "" {
		return "", errors.New(`@browser clear: missing target — a [n] ref from the last snapshot or a CSS selector`)
	}
	if err := r.Clear(ctx, inv.target); err != nil {
		return "", fmt.Errorf("@browser clear: %w", err)
	}
	return fmt.Sprintf(browserMsgClearedFmt, inv.target), nil
}

func browserReadForward(ctx context.Context, _ BrowserBackend, r BrowserReadBackend, _ browserInvocation) (string, error) {
	title, url, err := r.Forward(ctx)
	if err != nil {
		return "", fmt.Errorf("@browser forward: %w", err)
	}
	return fmt.Sprintf(browserMsgForwardFmt, title, url), nil
}

func browserReadReload(ctx context.Context, b BrowserBackend, r BrowserReadBackend, inv browserInvocation) (string, error) {
	title, url, err := r.Reload(ctx)
	if err != nil {
		return "", fmt.Errorf("@browser reload: %w", err)
	}
	return withSnapshot(ctx, b, inv, fmt.Sprintf(browserMsgReloadedFmt, title, url)), nil
}

func browserReadDownloads(_ context.Context, _ BrowserBackend, r BrowserReadBackend, _ browserInvocation) (string, error) {
	return renderDownloads(r.Downloads()), nil
}

// renderDownloads formats the download list for the model.
func renderDownloads(list []browser.DownloadInfo) string {
	if len(list) == 0 {
		return browserMsgNoDownloads
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, browserMsgDownloadsFmt, len(list))
	for _, d := range list {
		size := ""
		switch {
		case d.Total > 0:
			size = fmt.Sprintf(" %d/%d bytes", d.Received, d.Total)
		case d.Received > 0:
			size = fmt.Sprintf(" %d bytes", d.Received)
		}
		file := d.File
		if file == "" {
			file = "(path not known yet)"
		}
		fmt.Fprintf(&sb, "%s  %s%s  from %s\n", d.State, file, size, d.URL)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// browserCmdNetwork lists captured responses, optionally filtered by URL
// substring, and with --body fetches the body of the newest match.
func browserCmdNetwork(ctx context.Context, b BrowserBackend, inv browserInvocation) (string, error) {
	entries := b.NetworkTail(0)
	if f := strings.ToLower(strings.TrimSpace(inv.filter)); f != "" {
		kept := entries[:0:0]
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.URL), f) {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	if inv.tail > 0 && len(entries) > inv.tail {
		entries = entries[len(entries)-inv.tail:]
	}
	out := renderNetworkEntries(entries)
	if !inv.body {
		return out, nil
	}
	r, err := browserRead(b, "network --body")
	if err != nil {
		return "", err
	}
	entry, body, err := r.ResponseBody(ctx, inv.filter, inv.max)
	if err != nil {
		return "", fmt.Errorf("@browser network: %w", err)
	}
	return fmt.Sprintf("%s\n\nBody of %d %s %s:\n%s", out, entry.Status, entry.Method, entry.URL, body), nil
}
