/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * @mcp-login — authorize a remote (OAuth-protected) MCP server so its tools
 * become usable. When an mcp_* tool call fails because the server demands
 * authorization, the agent is told to invoke this tool with the server name;
 * it runs the OAuth authorization-code flow (opening a browser), stores the
 * token, and reconnects the server so its tools appear.
 *
 * Same architecture as @lsp/@knowledge: a thin schema + dispatch layer over a
 * package-level adapter supplied via SetMCPAuthAdapter; the cli package wires
 * the adapter over the live MCP manager.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// MCPAuthAdapter is the surface the @mcp-login tool needs from the live
// session. All results are pre-rendered, model-facing text.
type MCPAuthAdapter interface {
	// Login runs the interactive OAuth flow for server and reconnects it.
	Login(ctx context.Context, server string) (string, error)
	// Logout forgets a server's stored OAuth credential.
	Logout(server string) (string, error)
	// Status summarizes each server's authorization state.
	Status() (string, error)
}

// MCPServerAdapter is the optional part of the adapter behind restart and
// tools. Separate from MCPAuthAdapter so an adapter written against the
// original interface keeps compiling.
type MCPServerAdapter interface {
	// Restart stops and starts one server that is already in the user's
	// configuration. It never starts every server and never adds one.
	Restart(server string) (string, error)
	// Tools lists the tools one server exposes (all servers when empty).
	Tools(server string) (string, error)
}

type mcpAuthAdapterHolder struct{ a MCPAuthAdapter }

var mcpAuthAdapterAtom atomic.Value // mcpAuthAdapterHolder

// SetMCPAuthAdapter wires the live adapter. Called from the top-level cli
// package at startup; pass nil to detach.
func SetMCPAuthAdapter(a MCPAuthAdapter) {
	mcpAuthAdapterAtom.Store(mcpAuthAdapterHolder{a: a})
}

func currentMCPAuthAdapter() MCPAuthAdapter {
	v := mcpAuthAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(mcpAuthAdapterHolder)
	return h.a
}

// BuiltinMCPLoginPlugin is the @mcp-login tool.
type BuiltinMCPLoginPlugin struct{}

// NewBuiltinMCPLoginPlugin returns a ready-to-register plugin.
func NewBuiltinMCPLoginPlugin() *BuiltinMCPLoginPlugin { return &BuiltinMCPLoginPlugin{} }

// Name returns "@mcp-login".
func (*BuiltinMCPLoginPlugin) Name() string { return "@mcp-login" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinMCPLoginPlugin) Description() string {
	return "Authorize a remote OAuth-protected MCP server so its tools become usable. Call this when an mcp_* tool fails with an 'authorization required' error, or a server shows as needing login. It runs the OAuth flow (opens a browser for the user to approve), stores the token securely, refreshes it automatically thereafter, and reconnects the server. It can also restart one configured server that crashed or hung, and list a server's tools. Subcommands: login {server}, status, logout {server}, restart {server}, tools {server?}."
}

// Usage explains the canonical invocation forms.
func (*BuiltinMCPLoginPlugin) Usage() string {
	return `<tool_call name="@mcp-login" args='{"cmd":"login","args":{"server":"aws"}}' />

Subcommands:
  login  {server}   run the OAuth flow for the server and reconnect it (opens a browser)
  status            show each MCP server's authorization state
  logout {server}   forget a server's stored credential
  restart {server}  stop and start ONE configured server (crashed, hung, or
                    its tools went missing). Never all servers, never a server
                    the user stopped or that is not in their configuration
  tools  {server?}  list the tools a server exposes (every server when omitted)

When an mcp_* call returns "authorization required for server X", call:
  {"cmd":"login","args":{"server":"X"}}
then retry the original tool.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinMCPLoginPlugin) Version() string { return "1.1.0" }

// Path is empty for builtin plugins.
func (*BuiltinMCPLoginPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders
// into per-subcommand flag lists with examples.
func (*BuiltinMCPLoginPlugin) Schema() string {
	serverFlag := map[string]interface{}{"name": "server", "description": "name of the configured MCP server", "type": "string", "required": true}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "login",
				"description": "run the OAuth authorization flow for the server (opens a browser) and reconnect it so its tools become usable",
				"flags":       []map[string]interface{}{serverFlag},
				"examples":    []string{`{"cmd":"login","args":{"server":"aws"}}`},
			},
			{
				"name":        "status",
				"description": "show each MCP server's authorization state (connected / authorization required / logged in)",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"status"}`},
			},
			{
				"name":        "logout",
				"description": "forget a server's stored OAuth credential",
				"flags":       []map[string]interface{}{serverFlag},
				"examples":    []string{`{"cmd":"logout","args":{"server":"aws"}}`},
			},
			{
				"name":        "restart",
				"description": "stop and start one server from the user's MCP configuration — use it when the server crashed, hung, or its mcp_* tools disappeared. Refused for a server the user stopped and for names that are not configured; there is no way to restart every server",
				"flags":       []map[string]interface{}{serverFlag},
				"examples":    []string{`{"cmd":"restart","args":{"server":"filesystem"}}`},
			},
			{
				"name":        "tools",
				"description": "list the tools a server exposes, with their parameters (read-only)",
				"flags":       []map[string]interface{}{{"name": "server", "description": "name of the configured MCP server; omit for every server", "type": "string"}},
				"examples":    []string{`{"cmd":"tools","args":{"server":"filesystem"}}`},
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// Execute implements the plugin contract.
func (p *BuiltinMCPLoginPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream dispatches to the adapter. This tool produces no
// incremental output, so the stream callback is ignored.
func (p *BuiltinMCPLoginPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentMCPAuthAdapter()
	if adapter == nil {
		return "", errors.New("@mcp-login: MCP is not enabled in this session")
	}

	cmd, server, err := parseMCPLoginInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@mcp-login: %w", err)
	}

	switch cmd {
	case "login":
		if server == "" {
			return "", errors.New(`@mcp-login: "server" is required for login. Example: {"cmd":"login","args":{"server":"aws"}}`)
		}
		return adapter.Login(ctx, server)
	case "status":
		return adapter.Status()
	case "logout":
		if server == "" {
			return "", errors.New(`@mcp-login: "server" is required for logout`)
		}
		return adapter.Logout(server)
	case "restart", "tools":
		return mcpServerVerb(adapter, cmd, server)
	default:
		return "", errUnknownMCPLoginCmd(cmd)
	}
}

// mcpServerVerb runs restart / tools through the optional adapter surface.
func mcpServerVerb(adapter MCPAuthAdapter, cmd, server string) (string, error) {
	srv, ok := adapter.(MCPServerAdapter)
	if !ok {
		return "", fmt.Errorf("@mcp-login: %s is not available in this session", cmd)
	}
	if cmd == "tools" {
		return srv.Tools(server)
	}
	// "all" is how /mcp restart with no name behaves; here it must be an
	// explicit refusal, not a server lookup that happens to fail.
	if server == "" || strings.EqualFold(server, "all") || server == "*" {
		return "", errors.New(`@mcp-login: restart needs the name of ONE configured server, e.g. {"cmd":"restart","args":{"server":"filesystem"}}. Restarting every server is done by the user with /mcp restart`)
	}
	return srv.Restart(server)
}

// parseMCPLoginInvocation extracts the subcommand and server name from any of
// the shapes the agent may produce. It is deliberately lenient — strict
// parsing here just makes the model retry blindly:
//
//   - JSON envelope:  {"cmd":"logout","args":{"server":"aws"}}
//   - bare JSON:      {"server":"aws"}                       (implies login)
//   - positional:     logout aws  |  status  |  aws
//   - flag form:      logout --server aws  |  logout --server=aws
//     --cmd logout --server aws
//
// The flag form matters because the agent's tool flattener rewrites a
// {cmd,args} envelope into "--flag value" pairs; treating "--server" as the
// server name (the previous behavior) made every such call fail.
func parseMCPLoginInvocation(args []string) (cmd string, server string, err error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if payload == "" {
		return "status", "", nil
	}

	if strings.HasPrefix(payload, "{") {
		var raw struct {
			Cmd    string `json:"cmd"`
			Server string `json:"server"`
			Name   string `json:"name"`
			Args   struct {
				Server string `json:"server"`
				Name   string `json:"name"`
			} `json:"args"`
		}
		if uerr := json.Unmarshal([]byte(payload), &raw); uerr != nil {
			return "", "", fmt.Errorf(`parse envelope: %w. Expected {"cmd":"login","args":{"server":"aws"}}`, uerr)
		}
		server = firstNonEmptyStr(raw.Args.Server, raw.Args.Name, raw.Server, raw.Name)
		cmd = canonicalMCPLoginCmd(raw.Cmd)
		if cmd == "" && strings.TrimSpace(raw.Cmd) != "" {
			return "", "", errUnknownMCPLoginCmd(raw.Cmd)
		}
		if cmd == "" {
			if server != "" {
				return "login", server, nil
			}
			return "status", "", nil
		}
		return cmd, server, nil
	}

	// argv / flag form. Scan tokens, honoring --server / --cmd (and =forms),
	// and collect the rest as positionals.
	var positionals []string
	fields := strings.Fields(payload)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		key, val, hasEq := strings.Cut(f, "=")
		key = strings.TrimLeft(key, "-")
		switch strings.ToLower(key) {
		case "server", "name":
			if hasEq {
				server = trimQuotes(val)
			} else if i+1 < len(fields) {
				server = trimQuotes(fields[i+1])
				i++
			}
		case "cmd", "command", "action":
			if hasEq {
				cmd = canonicalMCPLoginCmd(val)
			} else if i+1 < len(fields) {
				cmd = canonicalMCPLoginCmd(fields[i+1])
				i++
			}
		default:
			if strings.HasPrefix(f, "-") {
				// Reported, not skipped: a dropped flag ran the verb with
				// less than the model asked for.
				return "", "", fmt.Errorf("unknown argument %q (valid: --server)", f)
			}
			positionals = append(positionals, trimQuotes(f))
		}
	}
	// "restart aws": two bare words whose first is no verb of this tool.
	// Reading it as a server name would start a login for "restart".
	// The same when the server came from a flag ("restart --server aws"):
	// a bare word that is neither a verb nor needed as the server name.
	if cmd == "" && len(positionals) > 0 && canonicalMCPLoginCmd(positionals[0]) == "" &&
		(len(positionals) >= 2 || server != "") {
		return "", "", errUnknownMCPLoginCmd(positionals[0])
	}
	for _, p := range positionals {
		if cmd == "" {
			if c := canonicalMCPLoginCmd(p); c != "" {
				cmd = c
				continue
			}
		}
		if server == "" {
			server = p
		}
	}
	if cmd == "" {
		if server != "" {
			cmd = "login"
		} else {
			cmd = "status"
		}
	}
	return cmd, server, nil
}

func canonicalMCPLoginCmd(cmd string) string {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "login", "auth", "authorize", "connect":
		return "login"
	case "status", "list", "ls":
		return "status"
	case "logout", "signout", "forget", "revoke":
		return "logout"
	case "restart", "reconnect":
		return "restart"
	case "tools":
		return "tools"
	default:
		return ""
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// errUnknownMCPLoginCmd rejects a verb this tool does not have. It used to
// fall through to "login", so asking to restart a server opened a browser
// for an OAuth flow nobody requested.
func errUnknownMCPLoginCmd(cmd string) error {
	return fmt.Errorf("@mcp-login: unknown cmd %q (valid: login|logout|status|restart|tools). Starting, stopping or reloading servers is done by the user with /mcp", strings.TrimSpace(cmd))
}
