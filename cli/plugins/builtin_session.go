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

// SessionReader is an OPTIONAL capability a SessionAdapter may implement to
// back the @session get subcommand — paginated reading of one saved
// conversation, so the model can pull past context without loading the
// session over the live one. Optional-interface pattern (like the memory
// tool's TimelineAccessor) so existing adapters keep compiling.
type SessionReader interface {
	// Get returns one page of the named session's messages. offset is
	// 0-based; limit <= 0 applies the default. A non-empty query centers the
	// page on the best-matching message instead of using offset.
	Get(ctx context.Context, name string, offset, limit int, query string) (string, error)
}

// SessionMessageReader is an OPTIONAL capability layered on top of
// SessionReader: fetch ONE message by absolute index with a much larger
// length ceiling than the paged view. Separate interface (not a new method
// on SessionReader) so existing adapters keep compiling.
type SessionMessageReader interface {
	// GetMessage returns the message at the 0-based absolute index of the
	// named session, nearly untruncated.
	GetMessage(ctx context.Context, name string, index int) (string, error)
}

// SessionWriter is an OPTIONAL capability a SessionAdapter may implement to
// back the NON-destructive write subcommands (save/fork/attach/detach) —
// same optional-interface pattern as SessionReader, so read-only adapters
// keep compiling. Each method returns the model-facing confirmation text.
//
// The surface is deliberately non-destructive: there is no delete and no
// forced load. In particular, Attach must NOT swap the in-memory history in
// the middle of an agent turn — the contract is that the adapter records the
// binding intent and the actual history convergence happens at the turn
// boundary (write-through / refresh); an immediate load is allowed only when
// the current conversation is empty.
type SessionWriter interface {
	// Save persists the current conversation under name and binds to it.
	Save(ctx context.Context, name string) (string, error)
	// Fork copies the current conversation (or its bound session file) into
	// a new session named name and binds to it.
	Fork(ctx context.Context, name string) (string, error)
	// Attach binds the conversation to the named session, creating it lazily
	// when it does not exist. See the interface comment for the mid-turn
	// history contract.
	Attach(ctx context.Context, name string) (string, error)
	// Detach removes the session binding, leaving the conversation unsaved.
	Detach(ctx context.Context) (string, error)
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
	return "Search, read and manage saved conversations: recall what was discussed earlier, list sessions, save or fork the current conversation under a name, and attach/detach the live session binding. Use when the user refers to a prior conversation ('what did we decide about X', 'continue where we left off') or asks to keep working under a named session."
}

// Usage explains the canonical invocation.
func (*BuiltinSessionPlugin) Usage() string {
	return `<tool_call name="@session" args='{"cmd":"search","args":{"query":"rate limiter design"}}' />

Subcommands (cmd + args):
  search {query, limit?}   search saved sessions; returns matching sessions + snippets
  get {name, offset?, limit?, query?, message?}   read one saved session page by page;
                           query jumps to the best match; message=<index> returns that
                           single message nearly in full (follow up on truncated entries)
  list                     list saved session names
  save {name}              save the current conversation under name and bind to it
                           (later turns write through to the store)
  fork {name}              fork the current conversation into a new session and bind to it
  attach {name}            bind to a named session; created lazily when missing. An
                           existing session's history is loaded only when the current
                           conversation is empty — otherwise the running conversation is
                           kept and both converge at the next turn boundary
  detach                   remove the session binding (the conversation stays in memory)

Sessions can NOT be deleted or destructively reloaded from this tool.

Typical recall flow: search finds WHICH session discussed something, get reads
the relevant part of THAT session, get with message=<index> expands the entries
that matter. To keep working under a name: save (new) or attach (existing).`
}

// Version is semver.
func (*BuiltinSessionPlugin) Version() string { return "1.2.0" }

// Path is empty for builtin plugins.
func (*BuiltinSessionPlugin) Path() string { return "" }

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
				"name":        "get",
				"description": "Read one saved session's messages page by page. Use after 'search' to pull the actual context from the matching session. A query centers the page on the best-matching message. Long messages are truncated in the page view — pass message=<index> to read a single message nearly in full.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Saved session name (from search or list)."},
					{"name": "offset", "type": "number", "required": false, "description": "0-based message offset (default 0)."},
					{"name": "limit", "type": "number", "required": false, "description": "Messages per page (default 20)."},
					{"name": "query", "type": "string", "required": false, "description": "Jump to the best-matching message instead of using offset."},
					{"name": "message", "type": "number", "required": false, "description": "Absolute message index: return THAT message alone, nearly untruncated (use when a page shows a cut-off entry you need whole)."},
				},
				"examples": []string{
					`{"cmd":"get","args":{"name":"auth-refactor","query":"refresh token"}}`,
					`{"cmd":"get","args":{"name":"auth-refactor","offset":20,"limit":20}}`,
					`{"cmd":"get","args":{"name":"auth-refactor","message":42}}`,
				},
			},
			{
				"name":        "list",
				"description": "List saved session names.",
				"examples":    []string{`{"cmd":"list"}`},
			},
			{
				"name":        "save",
				"description": "Save the current conversation under a name and bind to it; later turns write through to the saved session.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Session name to save the conversation under."},
				},
				"examples": []string{`{"cmd":"save","args":{"name":"auth-refactor"}}`},
			},
			{
				"name":        "fork",
				"description": "Fork the current conversation into a NEW session named name and bind to it. Fails if the target already exists.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Name for the forked session (must not exist yet)."},
				},
				"examples": []string{`{"cmd":"fork","args":{"name":"auth-refactor-v2"}}`},
			},
			{
				"name":        "attach",
				"description": "Bind the conversation to a named session (created lazily when missing). An existing session's history is loaded immediately only when the current conversation is empty; otherwise the binding takes effect at the turn boundary.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Session name to bind to."},
				},
				"examples": []string{`{"cmd":"attach","args":{"name":"auth-refactor"}}`},
			},
			{
				"name":        "detach",
				"description": "Remove the session binding; the conversation stays in memory but is no longer persisted to a named session.",
				"examples":    []string{`{"cmd":"detach"}`},
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
	case "get":
		reader, ok := adapter.(SessionReader)
		if !ok {
			return "", errors.New("@session get: session reading not available")
		}
		var in struct {
			Name    string          `json:"name"`
			Offset  json.RawMessage `json:"offset"`
			Limit   json.RawMessage `json:"limit"`
			Query   string          `json:"query"`
			Message json.RawMessage `json:"message"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Name) == "" {
			return "", errors.New(`@session get: "name" is required (use search or list first)`)
		}
		if len(in.Message) > 0 {
			mr, ok := adapter.(SessionMessageReader)
			if !ok {
				return "", errors.New("@session get: single-message reading not available")
			}
			return mr.GetMessage(ctx, in.Name, lenientInt(in.Message))
		}
		return reader.Get(ctx, in.Name, lenientInt(in.Offset), lenientInt(in.Limit), in.Query)
	case "list":
		return adapter.List(ctx)
	case "save", "fork", "attach", "detach":
		// Write ops require the optional SessionWriter capability. Note the
		// deliberate absence of delete/load here: the model manages bindings
		// and creates sessions, it never destroys or force-replaces them.
		writer, ok := adapter.(SessionWriter)
		if !ok {
			return "", fmt.Errorf("@session %s: session writing not available in this session", cmd)
		}
		if cmd == "detach" {
			return writer.Detach(ctx)
		}
		var in struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return "", fmt.Errorf(`@session %s: "name" is required`, cmd)
		}
		switch cmd {
		case "save":
			return writer.Save(ctx, name)
		case "fork":
			return writer.Fork(ctx, name)
		default:
			return writer.Attach(ctx, name)
		}
	default:
		return "", fmt.Errorf("@session: unknown cmd %q (valid: search|get|list|save|fork|attach|detach)", cmd)
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
			if !isFlatArgs(raw) {
				return "", "", fmt.Errorf("missing or unknown cmd %q (valid: search|get|list|save|fork|attach|detach)", cmdStr)
			}
			canon = "search" // flat native args, e.g. {"query":"..."}
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
	if len(args) == 0 {
		return "", "", fmt.Errorf("empty args")
	}
	canon := canonicalSessionCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	// The bare positional maps to the subcommand's natural primary key:
	// write ops take a session name, the read ops keep the historic "query".
	primary := "query"
	switch canon {
	case "save", "fork", "attach":
		primary = "name"
	}
	return canon, argvInner(args[1:], primary, nil, map[string]bool{"limit": true}), nil
}

// canonicalSessionCmd maps model spellings to the canonical subcommand.
// "delete"/"load" are intentionally unmapped: the tool surface is
// non-destructive by design, so those fall through to the unknown-cmd error.
func canonicalSessionCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "search", "find", "recall":
		return "search"
	case "get", "read", "show", "open":
		return "get"
	case "list", "sessions":
		return "list"
	case "save":
		return "save"
	case "fork", "branch":
		return "fork"
	case "attach", "bind":
		return "attach"
	case "detach", "unbind":
		return "detach"
	}
	return ""
}
