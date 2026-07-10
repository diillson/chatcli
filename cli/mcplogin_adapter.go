/*
 * ChatCLI - Adapter binding the @mcp-login tool to the live MCP manager.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.MCPAuthAdapter over cli.mcpManager: login runs the OAuth
 * authorization-code flow and reconnects the server; status and logout report
 * and forget per-server credentials. Wired via plugins.SetMCPAuthAdapter at
 * startup.
 */
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
)

// mcpLoginAdapter is the concrete plugins.MCPAuthAdapter.
type mcpLoginAdapter struct {
	cli *ChatCLI
}

// mcpLoginTimeout bounds the whole interactive login (discovery + registration
// + browser approval + token exchange + reconnect). It is a bit above the
// internal 5-minute callback wait so the browser step is the limiting factor.
const mcpLoginTimeout = 6 * time.Minute

// Login implements plugins.MCPAuthAdapter.
func (a *mcpLoginAdapter) Login(ctx context.Context, server string) (string, error) {
	if a.cli.mcpManager == nil {
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.not_enabled"))
	}
	server = strings.TrimSpace(server)

	// Give the interactive browser step enough headroom while still aborting
	// if the caller's context (the session) is cancelled.
	loginCtx, cancel := context.WithTimeout(ctx, mcpLoginTimeout)
	defer cancel()

	if err := a.cli.mcpManager.LoginOAuth(loginCtx, server); err != nil {
		return "", err
	}

	// Report the post-login tool count so the agent knows the server is ready.
	tools := 0
	for _, s := range a.cli.mcpManager.GetServerStatus() {
		if s.Name == server {
			tools = s.ToolCount
			break
		}
	}
	return i18n.T("mcp.oauth.login_success", server, tools), nil
}

// Logout implements plugins.MCPAuthAdapter.
func (a *mcpLoginAdapter) Logout(server string) (string, error) {
	if a.cli.mcpManager == nil {
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.not_enabled"))
	}
	server = strings.TrimSpace(server)
	if err := a.cli.mcpManager.LogoutOAuth(context.Background(), server); err != nil {
		return "", err
	}
	return i18n.T("mcp.oauth.logout_success", server), nil
}

// Status implements plugins.MCPAuthAdapter — a compact per-server summary.
func (a *mcpLoginAdapter) Status() (string, error) {
	if a.cli.mcpManager == nil {
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.not_enabled"))
	}
	statuses := a.cli.mcpManager.GetServerStatus()
	if len(statuses) == 0 {
		return i18n.T("mcp.oauth.status_no_servers"), nil
	}
	var b strings.Builder
	b.WriteString(i18n.T("mcp.oauth.status_header"))
	b.WriteString("\n")
	for _, s := range statuses {
		state := i18n.T("mcp.oauth.status_connected")
		switch {
		case s.AuthRequired:
			state = i18n.T("mcp.oauth.status_auth_required")
		case s.Connected:
			// keep connected
		case a.cli.mcpManager.HasOAuthCredential(s.Name):
			state = i18n.T("mcp.oauth.status_logged_in_disconnected")
		default:
			state = i18n.T("mcp.oauth.status_disconnected")
		}
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, state)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// compile-time assertion that the adapter satisfies the plugin interface.
var _ plugins.MCPAuthAdapter = (*mcpLoginAdapter)(nil)
