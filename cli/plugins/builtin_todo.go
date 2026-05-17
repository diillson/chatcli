/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/diillson/chatcli/i18n"
)

// TodoAdapter is the interface the @todo plugin uses to manipulate the
// live agent task tracker. The cli package wires an implementation
// bound to the current AgentMode session at run start; the plugin
// stays decoupled from cli/agent.* concrete types.
//
// Design mirrors Claude Code's TodoWrite tool: the LLM submits the
// full list of todos on each call (Write), or asks for the current
// state (List), or flips a single item by id (Mark). The replacement-
// semantics path is the canonical one — it makes the LLM the single
// source of truth for the plan, eliminating "tracker drift" where the
// model and the tracker disagree after a few turns.
type TodoAdapter interface {
	// Write replaces the entire plan with the supplied items.
	// Returns the post-write progress summary.
	Write(items []TodoItem) (string, error)

	// List returns the current plan's progress summary.
	List() (string, error)

	// Mark sets the status of one task by its 1-indexed ID. Returns
	// an error when the id is out of range.
	Mark(id int, status string, errorMsg string) (string, error)
}

// TodoItem is the flat, LLM-facing view of a single planned task.
// Decoupled from the agent.TaskTracker.TaskSpec type so the plugins
// package stays out of the cli/agent dependency graph.
type TodoItem struct {
	Description string `json:"description"`
	Status      string `json:"status,omitempty"` // pending | in_progress | completed | failed
}

// todoAdapterMu guards the package-level todoAdapter pointer. A
// sync.Mutex (not atomic.Value) is intentional: atomic.Value rejects
// nil and consistent-concrete-type stores, both of which conflict with
// "set to live adapter, then explicitly unwire in tests / on shutdown".
// The contention surface is minimal — the adapter is read once per
// @todo invocation and written once at startup.
var (
	todoAdapterMu sync.RWMutex
	todoAdapter   TodoAdapter
)

// SetTodoAdapter wires the live adapter. Called from cli.NewChatCLI
// after the AgentMode is constructed; subsequent calls replace the
// adapter under the package mutex. Passing nil explicitly unwires —
// useful in tests and at process shutdown.
func SetTodoAdapter(a TodoAdapter) {
	todoAdapterMu.Lock()
	defer todoAdapterMu.Unlock()
	todoAdapter = a
}

// currentTodoAdapter returns the wired adapter or nil when not yet set.
func currentTodoAdapter() TodoAdapter {
	todoAdapterMu.RLock()
	defer todoAdapterMu.RUnlock()
	return todoAdapter
}

// BuiltinTodoPlugin exposes the agent's task tracker as an LLM-callable
// tool (Claude Code TodoWrite parity). Letting the model own its plan
// reduces ReAct loop drift: every turn the model sees the same plan it
// wrote, and reconciles in one place rather than re-deriving from prior
// turns.
type BuiltinTodoPlugin struct{}

// NewBuiltinTodoPlugin returns the singleton.
func NewBuiltinTodoPlugin() *BuiltinTodoPlugin { return &BuiltinTodoPlugin{} }

// Name is the LLM-visible tool name.
func (p *BuiltinTodoPlugin) Name() string { return "@todo" }

// Description is shown in the model's tool catalog.
func (p *BuiltinTodoPlugin) Description() string {
	return i18n.T("plugins.todo.description")
}

// Usage is the short shell-like example for /help.
func (p *BuiltinTodoPlugin) Usage() string { return "@todo write|list|mark" }

// Version follows semver tied to the tracker contract.
func (p *BuiltinTodoPlugin) Version() string { return "1.0.0" }

// Path is the builtin sentinel.
func (p *BuiltinTodoPlugin) Path() string { return "[builtin]" }

// Schema returns the JSON schema. Three subcommands:
//
//   - write: replace the entire plan with todos[].
//   - list:  return the current progress.
//   - mark:  flip a single task by id (1-indexed).
func (p *BuiltinTodoPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON",
		"subcommands": []map[string]interface{}{
			{
				"name":        "write",
				"description": "Replace the agent's task plan with the supplied list. Each call should send the FULL updated plan, not a delta. This is the canonical TodoWrite path.",
				"flags": []map[string]interface{}{
					{"name": "todos", "type": "array", "required": true, "description": "Array of {description,status?} objects (status: pending|in_progress|completed|failed)"},
				},
				"examples": []string{
					`{"cmd":"write","args":{"todos":[{"description":"Investigate issue","status":"completed"},{"description":"Apply fix","status":"in_progress"},{"description":"Add tests","status":"pending"}]}}`,
				},
			},
			{
				"name":        "list",
				"description": "Return the current task plan with status icons and progress counts.",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"list"}`},
			},
			{
				"name":        "mark",
				"description": "Update the status of one task by its 1-indexed ID. Use this for single-item updates when the rest of the plan is unchanged.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "integer", "required": true, "description": "1-indexed task id"},
					{"name": "status", "type": "string", "required": true, "description": "pending|in_progress|completed|failed"},
					{"name": "error", "type": "string", "description": "Optional error message when status=failed"},
				},
				"examples": []string{`{"cmd":"mark","args":{"id":2,"status":"completed"}}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute is the legacy synchronous entry-point.
func (p *BuiltinTodoPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream dispatches the subcommand to the wired adapter.
// The stream callback is unused — todo operations are atomic and
// produce a single result string, not an incremental log.
func (p *BuiltinTodoPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentTodoAdapter()
	if adapter == nil {
		return "", errors.New("@todo: no adapter wired (agent loop not active)")
	}

	sub, payload, err := parseTodoInvocation(args)
	if err != nil {
		return "", err
	}

	switch sub {
	case "write":
		items, err := todoItemsFromPayload(payload)
		if err != nil {
			return "", err
		}
		return adapter.Write(items)
	case "list", "":
		return adapter.List()
	case "mark":
		id := jsonInt(payload, "id")
		if id <= 0 {
			return "", errors.New("@todo mark: id is required (1-indexed)")
		}
		status := strings.TrimSpace(jsonString(payload, "status"))
		if status == "" {
			return "", errors.New("@todo mark: status is required (pending|in_progress|completed|failed)")
		}
		errMsg := jsonString(payload, "error", "errorMsg", "errMsg")
		return adapter.Mark(id, status, errMsg)
	default:
		return "", fmt.Errorf("@todo: unknown subcommand %q (expected write|list|mark)", sub)
	}
}

// parseTodoInvocation extracts the subcommand name and the inner
// args map from the LLM's envelope. Supports:
//
//   - `{"cmd":"write","args":{...}}` — canonical
//   - `{"cmd":"list"}`               — no args
//   - `["list"]`                      — positional fallback
//
// Returns (subcommand, innerArgs, error). innerArgs is nil for
// subcommands with no payload.
func parseTodoInvocation(args []string) (string, map[string]json.RawMessage, error) {
	if len(args) == 0 {
		// No args = list (read-only default).
		return "list", nil, nil
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(first), &top); err != nil {
			return "", nil, fmt.Errorf("@todo: malformed JSON args: %w", err)
		}
		var sub string
		if cmd, ok := top["cmd"]; ok {
			_ = json.Unmarshal(cmd, &sub)
		}
		if sub == "" {
			sub = "list"
		}
		var inner map[string]json.RawMessage
		if rawInner, ok := top["args"]; ok {
			_ = json.Unmarshal(rawInner, &inner)
		}
		return sub, inner, nil
	}
	// Positional. First token is the subcommand.
	return first, nil, nil
}

// todoItemsFromPayload decodes a {"todos":[...]} array into typed items.
// Each item is validated for status (rejected if not one of the four
// canonical values).
func todoItemsFromPayload(payload map[string]json.RawMessage) ([]TodoItem, error) {
	raw, ok := payload["todos"]
	if !ok {
		return nil, errors.New("@todo write: todos array is required")
	}
	var items []TodoItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("@todo write: invalid todos array: %w", err)
	}
	if len(items) == 0 {
		return nil, errors.New("@todo write: todos array cannot be empty")
	}
	for i := range items {
		items[i].Description = strings.TrimSpace(items[i].Description)
		if items[i].Description == "" {
			return nil, fmt.Errorf("@todo write: todo #%d has empty description", i+1)
		}
		switch items[i].Status {
		case "", "pending", "in_progress", "completed", "failed":
			// ok
		default:
			return nil, fmt.Errorf("@todo write: todo #%d has invalid status %q (expected pending|in_progress|completed|failed)",
				i+1, items[i].Status)
		}
	}
	return items, nil
}
