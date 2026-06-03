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
	"strings"

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
