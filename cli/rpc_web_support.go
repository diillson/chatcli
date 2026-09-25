/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"sort"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/ui/theme"
)

// The getters below give a remote surface (the web UI) structured access to
// state the REPL only prints: cost, budget, MCP servers, saved sessions and
// the theme. They read; changing state stays with the commands.

// CostSnapshotRPC returns the session's cost ledger; ok is false when cost
// tracking is off.
func (cli *ChatCLI) CostSnapshotRPC() (SessionCostData, bool) {
	if cli.costTracker == nil {
		return SessionCostData{}, false
	}
	return cli.costTracker.Snapshot(), true
}

// DailyBudgetRPC reports today's spend and the configured daily limit (0
// when none).
func (cli *ChatCLI) DailyBudgetRPC() (spentUSD, limitUSD float64) {
	return cli.costTracker.DailyBudget()
}

// BudgetBlockedRPC reports whether the budget hard stop is holding turns.
func (cli *ChatCLI) BudgetBlockedRPC() bool {
	return cli.costTracker != nil && cli.costTracker.BudgetBlocked()
}

// MCPServerStatusRPC is one configured MCP server as the web UI shows it.
type MCPServerStatusRPC struct {
	Name         string    `json:"name"`
	Connected    bool      `json:"connected"`
	ToolCount    int       `json:"tool_count"`
	AuthRequired bool      `json:"auth_required"`
	LastError    string    `json:"last_error,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
}

// MCPStatusRPC lists the MCP servers and their connection state.
func (cli *ChatCLI) MCPStatusRPC() []MCPServerStatusRPC {
	if cli.mcpManager == nil {
		return nil
	}
	statuses := cli.mcpManager.GetServerStatus()
	out := make([]MCPServerStatusRPC, 0, len(statuses))
	for _, s := range statuses {
		row := MCPServerStatusRPC{Name: s.Name, Connected: s.Connected, ToolCount: s.ToolCount, AuthRequired: s.AuthRequired, StartedAt: s.StartedAt}
		if s.LastError != nil {
			row.LastError = s.LastError.Error()
		}
		out = append(out, row)
	}
	return out
}

// SessionSummaryRPC is one saved session in the catalog.
type SessionSummaryRPC struct {
	Name     string    `json:"name"`
	Title    string    `json:"title,omitempty"`
	Modified time.Time `json:"modified"`
}

// SessionCatalogRPC lists the saved sessions with their titles and last
// modification, newest first.
func (cli *ChatCLI) SessionCatalogRPC() []SessionSummaryRPC {
	if cli.sessionManager == nil {
		return nil
	}
	names, err := cli.sessionManager.ListSessions()
	if err != nil {
		return nil
	}
	titles := cli.sessionManager.SessionTitles()
	out := make([]SessionSummaryRPC, 0, len(names))
	for _, name := range names {
		row := SessionSummaryRPC{Name: name, Title: titles[name]}
		if mt, err := cli.sessionManager.SessionModTime(name); err == nil {
			row.Modified = mt
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out
}

// SessionMessagesRPC pages through a saved session's chat history and
// reports its total length.
func (cli *ChatCLI) SessionMessagesRPC(name string, offset, limit int) ([]models.Message, int, error) {
	if cli.sessionManager == nil {
		return nil, 0, fmt.Errorf("%s", i18n.T("rpc.session.store_unavailable"))
	}
	if err := validateSessionName(name); err != nil {
		return nil, 0, err
	}
	return cli.sessionManager.GetSessionMessages(name, offset, limit)
}

// MaxTokensRPC returns the per-session max tokens override (0 = catalog).
func (cli *ChatCLI) MaxTokensRPC() int { return cli.UserMaxTokens }

// ActiveThemeCSSVars maps the active theme to CSS variables for a light
// page; nil for dark themes, which the pages render with their own tokens.
func ActiveThemeCSSVars() map[string]string { return dashThemeVars(theme.Active()) }

// ActiveThemeName is the name of the theme the process runs with.
func ActiveThemeName() string { return theme.Active().Name }

// LaunchBrowser opens rawURL in the user's default browser.
func LaunchBrowser(rawURL string) error { return openBrowserURL(rawURL) }
