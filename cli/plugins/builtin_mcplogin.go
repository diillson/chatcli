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
	return "Authorize a remote OAuth-protected MCP server so its tools become usable. Call this when an mcp_* tool fails with an 'authorization required' error, or a server shows as needing login. It runs the OAuth flow (opens a browser for the user to approve), stores the token securely, refreshes it automatically thereafter, and reconnects the server. Subcommands: login {server}, status, logout {server}."
}

// Usage explains the canonical invocation forms.
func (*BuiltinMCPLoginPlugin) Usage() string {
	return `<tool_call name="@mcp-login" args='{"cmd":"login","args":{"server":"aws"}}' />

Subcommands:
  login  {server}   run the OAuth flow for the server and reconnect it (opens a browser)
  status            show each MCP server's authorization state
  logout {server}   forget a server's stored credential

When an mcp_* call returns "authorization required for server X", call:
  {"cmd":"login","args":{"server":"X"}}
then retry the original tool.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinMCPLoginPlugin) Version() string { return "1.0.0" }

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
	default:
		return "", fmt.Errorf("@mcp-login: unknown cmd %q (valid: login|status|logout)", cmd)
	}
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
				continue // unknown flag — ignore
			}
			positionals = append(positionals, trimQuotes(f))
		}
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
