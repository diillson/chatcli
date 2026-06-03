/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinSessionPlugin — search past conversations as an @session ReAct tool.
 *
 * It lets the agent recall what was discussed in earlier saved sessions
 * ("what did we decide about the cache last week?") by searching ChatCLI's own
 * saved-session store. Inspired by hermes-agent's session_search tool;
 * implemented natively against ChatCLI's SessionManager via an adapter.
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

// SessionAdapter exposes saved-session search to the @session tool.
type SessionAdapter interface {
	// Search returns a formatted list of matching sessions with snippets.
	Search(ctx context.Context, query string, limit int) (string, error)
	// List returns the saved session names.
	List(ctx context.Context) (string, error)
}

type sessionAdapterHolder struct{ a SessionAdapter }

var sessionAdapterAtom atomic.Value // stores sessionAdapterHolder

// SetSessionAdapter wires the live adapter; pass nil to clear.
func SetSessionAdapter(a SessionAdapter) { sessionAdapterAtom.Store(sessionAdapterHolder{a: a}) }

func currentSessionAdapter() SessionAdapter {
	v := sessionAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(sessionAdapterHolder)
	return h.a
}

// BuiltinSessionPlugin is the @session tool.
type BuiltinSessionPlugin struct{}

// NewBuiltinSessionPlugin returns a ready-to-register plugin.
func NewBuiltinSessionPlugin() *BuiltinSessionPlugin { return &BuiltinSessionPlugin{} }

// Name returns "@session".
func (*BuiltinSessionPlugin) Name() string { return "@session" }

// Description surfaces the tool.
func (*BuiltinSessionPlugin) Description() string {
	return "Search past saved conversations and recall what was discussed earlier, or list saved sessions. Use when the user refers to a prior conversation ('what did we decide about X', 'continue where we left off')."
}

// Usage explains the canonical invocation.
func (*BuiltinSessionPlugin) Usage() string {
	return `<tool_call name="@session" args='{"cmd":"search","args":{"query":"rate limiter design"}}' />

Subcommands (cmd + args):
  search {query, limit?}   search saved sessions; returns matching sessions + snippets
  list                     list saved session names`
}

// Version is semver.
func (*BuiltinSessionPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinSessionPlugin) Path() string { return "" }

// IsConcurrencySafe — read-only over the session store.
func (*BuiltinSessionPlugin) IsConcurrencySafe() bool { return true }

// Schema describes the subcommands.
func (*BuiltinSessionPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "search",
				"description": "Search saved conversations for a query; returns matching sessions with snippets.",
				"flags": []map[string]interface{}{
					{"name": "query", "type": "string", "required": true, "description": "Free-text search across saved sessions."},
					{"name": "limit", "type": "number", "required": false, "description": "Max snippets per session (default 3)."},
				},
				"examples": []string{`{"cmd":"search","args":{"query":"auth refactor"}}`},
			},
			{
				"name":        "list",
				"description": "List saved session names.",
				"examples":    []string{`{"cmd":"list"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinSessionPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback.
func (p *BuiltinSessionPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentSessionAdapter()
	if adapter == nil {
		return "", errors.New("@session: session search is not available in this session")
	}
	if len(args) == 0 {
		return "", errors.New(`@session: empty args. Example: <tool_call name="@session" args='{"cmd":"search","args":{"query":"..."}}' />`)
	}

	cmd, inner, err := parseSessionInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@session: %w", err)
	}

	switch cmd {
	case "search":
		var in struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Query) == "" {
			return "", errors.New(`@session search: "query" is required`)
		}
		if in.Limit <= 0 {
			in.Limit = 3
		}
		return adapter.Search(ctx, in.Query, in.Limit)
	case "list":
		return adapter.List(ctx)
	default:
		return "", fmt.Errorf("@session: unknown cmd %q (valid: search|list)", cmd)
	}
}

// parseSessionInvocation mirrors the other builtins' parsing.
func parseSessionInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf("parse envelope: %w", err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalSessionCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: search|list)", cmdStr)
		}
		var inner string
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner = string(rargs)
		} else {
			delete(raw, "cmd")
			b, _ := json.Marshal(raw)
			inner = string(b)
		}
		return canon, inner, nil
	}
	canon := canonicalSessionCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	if canon == "search" {
		rest := strings.TrimSpace(strings.TrimPrefix(payload, args[0]))
		b, _ := json.Marshal(map[string]string{"query": rest})
		return canon, string(b), nil
	}
	return canon, "{}", nil
}

func canonicalSessionCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "search", "find", "recall":
		return "search"
	case "list", "sessions":
		return "list"
	}
	return ""
}
