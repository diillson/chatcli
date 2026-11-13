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
	// Login reconnects the server, so it is a start like any other.
	if a.cli.mcpStoppedByUser(server) {
		return "", fmt.Errorf("%s", i18n.T("mcp.tool.restart_user_stopped", server, server))
	}

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
	// The MCP tool catalog is part of the stable prompt prefix (/mcp login
	// notes the same).
	a.cli.notePrefixChanged("mcp login")
	return i18n.T("mcp.oauth.login_success", server, tools), nil
}

// Restart implements plugins.MCPServerAdapter: one named server from the
// user's configuration. A server the user stopped stays stopped — the model
// recovers crashes, it does not overrule /mcp stop.
func (a *mcpLoginAdapter) Restart(server string) (string, error) {
	if a.cli.mcpManager == nil {
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.not_enabled"))
	}
	server = strings.TrimSpace(server)
	if !a.cli.mcpServerExists(server) {
		return "", fmt.Errorf("%s", i18n.T("mcp.cmd.unknown_server", server))
	}
	if a.cli.mcpStoppedByUser(server) {
		return "", fmt.Errorf("%s", i18n.T("mcp.tool.restart_user_stopped", server, server))
	}
	err := a.cli.mcpRestartServer(server)
	// Noted on failure too: the stop half already removed the server's
	// tools from the catalog, which is part of the stable prompt prefix.
	a.cli.notePrefixChanged("mcp restart")
	if err != nil {
		return "", fmt.Errorf("%s", i18n.T("mcp.cmd.restart_error", translateMCPError(err)))
	}
	tools := 0
	for _, s := range a.cli.mcpManager.GetServerStatus() {
		if s.Name == server {
			tools = s.ToolCount
		}
	}
	return i18n.T("mcp.tool.restart_ok", server, tools), nil
}

// Tools implements plugins.MCPServerAdapter: the model-readable form of
// /mcp tools.
func (a *mcpLoginAdapter) Tools(server string) (string, error) {
	if a.cli.mcpManager == nil {
		return "", fmt.Errorf("%s", i18n.T("mcp.oauth.not_enabled"))
	}
	server = strings.TrimSpace(server)
	if server != "" && !a.cli.mcpServerExists(server) {
		return "", fmt.Errorf("%s", i18n.T("mcp.cmd.unknown_server", server))
	}
	wantPrefix := ""
	if server != "" {
		wantPrefix = fmt.Sprintf("[MCP:%s]", server)
	}
	var b strings.Builder
	for _, t := range a.cli.mcpManager.GetTools() {
		if !strings.HasPrefix(t.Function.Description, wantPrefix) {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", t.Function.Name, t.Function.Description)
		if props, ok := t.Function.Parameters["properties"].(map[string]interface{}); ok && len(props) > 0 {
			fmt.Fprintf(&b, "  params: %s\n", strings.Join(ctxSortedKeys(props), ", "))
		}
	}
	if b.Len() == 0 {
		if server == "" {
			return i18n.T("mcp.cmd.no_tools"), nil
		}
		return i18n.T("mcp.cmd.no_tools_for_server", server), nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
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
	a.cli.notePrefixChanged("mcp logout")
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
var _ plugins.MCPServerAdapter = (*mcpLoginAdapter)(nil)
