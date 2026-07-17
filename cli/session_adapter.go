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
		fmt.Fprintf(&b, "\n• %s (%s)\n", h.Session, i18n.T("session.tool.match_count", h.Matches))
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

// getMessageCap bounds one message's rendered length so a single giant tool
// dump inside an old session cannot blow the reply budget.
const getMessageCap = 600

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
		content := strings.TrimSpace(m.Content)
		if len(content) > getMessageCap {
			content = content[:getMessageCap] + "…"
		}
		fmt.Fprintf(&b, "\n[%d] %s: %s\n", offset+i, m.Role, content)
	}
	if next := offset + len(page); next < total {
		b.WriteByte('\n')
		b.WriteString(i18n.T("session.tool.get.next", next))
	}
	return strings.TrimRight(b.String(), "\n"), nil
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
	var b strings.Builder
	b.WriteString(i18n.T("session.tool.list.header"))
	b.WriteByte('\n')
	for _, n := range names {
		b.WriteString("  • " + n + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
