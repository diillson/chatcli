/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - session_adapter.go
 *
 * Implements plugins.SessionAdapter so the @session tool can search the saved
 * conversation store through the live SessionManager. Supplied to
 * plugins.SetSessionAdapter at startup.
 */
package cli

import (
	"context"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/i18n"
)

// sessionPluginAdapter is the concrete plugins.SessionAdapter.
type sessionPluginAdapter struct {
	cli *ChatCLI
}

// Search runs a free-text search across saved sessions.
func (a *sessionPluginAdapter) Search(_ context.Context, query string, limit int) (string, error) {
	if a.cli == nil || a.cli.sessionManager == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	hits, err := a.cli.sessionManager.SearchSessions(query, limit)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return i18n.T("session.tool.no_match", query), nil
	}

	var b strings.Builder
	b.WriteString(i18n.T("session.tool.match_header", query))
	b.WriteByte('\n')
	for _, h := range hits {
		label := h.Session
		if h.Title != "" {
			label += " — " + h.Title
		}
		fmt.Fprintf(&b, "\n• %s (%s)\n", label, i18n.T("session.tool.match_count", h.Matches))
		for _, sn := range h.Snippets {
			sn = strings.TrimSpace(sn)
			if sn != "" {
				b.WriteString("    … " + sn + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// getPageLimit is the default @session get page size, in messages.
const getPageLimit = 20

// Role-aware caps for one message's rendered length inside a page. Tool
// dumps stay tightly bounded (a single giant command output cannot blow the
// reply budget), but user/assistant prose is exactly what a recap needs —
// the old flat 600-char cap on assistant analysis messages was the "partial
// recall" failure mode. A single message fetched by index gets the largest
// ceiling.
const (
	getMessageCapTool   = 600
	getMessageCapProse  = 2000
	getSingleMessageCap = 8000
)

// getMessageCapFor returns the page-render cap for a message role.
func getMessageCapFor(role string) int {
	switch strings.ToLower(role) {
	case "tool", "function", "system":
		return getMessageCapTool
	default:
		return getMessageCapProse
	}
}

// Get implements plugins.SessionReader: one page of a saved session's
// messages, with absolute indices so the model can paginate deterministically.
// A non-empty query centers the page on the BM25-best-matching message.
func (a *sessionPluginAdapter) Get(_ context.Context, name string, offset, limit int, query string) (string, error) {
	if a.cli == nil || a.cli.sessionManager == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	if limit <= 0 {
		limit = getPageLimit
	}
	sm := a.cli.sessionManager

	if q := strings.TrimSpace(query); q != "" {
		all, _, err := sm.GetSessionMessages(name, 0, math.MaxInt32)
		if err != nil {
			return "", err
		}
		contents := make([]string, len(all))
		for i, m := range all {
			contents[i] = m.Content
		}
		if hits := ctxmgr.RankDocsBM25(contents, q, 1); len(hits) > 0 {
			offset = hits[0].Index - limit/2
			if offset < 0 {
				offset = 0
			}
		}
	}

	page, total, err := sm.GetSessionMessages(name, offset, limit)
	if err != nil {
		return "", err
	}
	if len(page) == 0 {
		return i18n.T("session.tool.get.empty", name), nil
	}

	var b strings.Builder
	b.WriteString(i18n.T("session.tool.get.header", name, offset, offset+len(page)-1, total))
	b.WriteByte('\n')
	for i, m := range page {
		content := truncateRunesafe(strings.TrimSpace(m.Content), getMessageCapFor(m.Role))
		fmt.Fprintf(&b, "\n[%d] %s: %s\n", offset+i, m.Role, content)
	}
	if next := offset + len(page); next < total {
		b.WriteByte('\n')
		b.WriteString(i18n.T("session.tool.get.next", next))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// GetMessage implements plugins.SessionMessageReader: one message, by
// absolute index, nearly whole — the read-in-full escape hatch after a
// paged Get showed a truncated entry the model actually needs.
func (a *sessionPluginAdapter) GetMessage(_ context.Context, name string, index int) (string, error) {
	if a.cli == nil || a.cli.sessionManager == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	all, total, err := a.cli.sessionManager.GetSessionMessages(name, 0, math.MaxInt32)
	if err != nil {
		return "", err
	}
	if index < 0 || index >= total {
		return "", fmt.Errorf("%s", i18n.T("session.tool.get.bad_index", index, total, name))
	}
	m := all[index]
	content := truncateRunesafe(strings.TrimSpace(m.Content), getSingleMessageCap)
	header := i18n.T("session.tool.get.one_header", name, index, m.Role, total)
	return header + "\n\n" + content, nil
}

// truncateRunesafe cuts s at cap bytes, snapping back to a rune boundary so
// the output stays valid UTF-8, and appends an ellipsis when it cut.
func truncateRunesafe(s string, capBytes int) string {
	if len(s) <= capBytes {
		return s
	}
	cut := capBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// List returns the saved session names.
func (a *sessionPluginAdapter) List(_ context.Context) (string, error) {
	if a.cli == nil || a.cli.sessionManager == nil {
		return "", fmt.Errorf("%s", i18n.T("session.tool.unavailable"))
	}
	names, err := a.cli.sessionManager.ListSessions()
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return i18n.T("session.tool.list.empty"), nil
	}
	titles := a.cli.sessionManager.SessionTitles()
	var b strings.Builder
	b.WriteString(i18n.T("session.tool.list.header"))
	b.WriteByte('\n')
	for _, n := range names {
		line := "  • " + n
		if t := titles[n]; t != "" {
			line += " — " + t
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
