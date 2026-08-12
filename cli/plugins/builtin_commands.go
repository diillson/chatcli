/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinCommandsPlugin — the model-facing side of the slash-command catalog
 * (.chatcli/commands + ~/.chatcli/commands + Claude Code / Devin interop).
 *
 * Users invoke commands as "/name args"; the MODEL discovers and reuses the
 * same playbooks through this tool: @commands list shows what the team has
 * codified, @commands get expands one template (arguments interpolated,
 * pre-execution lines resolved through the security gate) so the model can
 * follow the workflow mid-task. This closes the loop the user asked for:
 * a command is team knowledge, and the agent should be able to reach for it
 * exactly like a human does — on every surface (REPL, gateway, ACP, MCP).
 *
 * Like @knowledge and @recall, the live catalog is owned by the top-level
 * ChatCLI and reached through a package-level adapter.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
)

// CommandsAdapter is the interface the @commands builtin uses to reach the
// live slash-command catalog.
type CommandsAdapter interface {
	// List renders the catalog (names, descriptions, argument hints).
	List() string
	// Get expands one command with the given argument string. ok=false when
	// the name is unknown.
	Get(ctx context.Context, name, args string) (string, bool)
}

type commandsAdapterHolder struct{ a CommandsAdapter }

var commandsAdapterAtom atomic.Value // stores commandsAdapterHolder

// SetCommandsAdapter wires the live adapter (nil clears it).
func SetCommandsAdapter(a CommandsAdapter) {
	commandsAdapterAtom.Store(commandsAdapterHolder{a: a})
}

func currentCommandsAdapter() CommandsAdapter {
	v := commandsAdapterAtom.Load()
	if v == nil {
		return nil
	}
	return v.(commandsAdapterHolder).a
}

// BuiltinCommandsPlugin is the @commands tool.
type BuiltinCommandsPlugin struct{}

// NewBuiltinCommandsPlugin returns a ready-to-register plugin.
func NewBuiltinCommandsPlugin() *BuiltinCommandsPlugin { return &BuiltinCommandsPlugin{} }

// Name returns "@commands".
func (*BuiltinCommandsPlugin) Name() string { return "@commands" }

// Description surfaces the tool in the agent catalog.
func (*BuiltinCommandsPlugin) Description() string {
	return "Discover and expand the project's slash-command templates (reusable team playbooks from .chatcli/commands, ~/.chatcli/commands, .claude/commands and .devin/workflows): list the catalog, or get one command expanded with arguments to follow its workflow"
}

// Usage explains the canonical invocation forms.
func (*BuiltinCommandsPlugin) Usage() string {
	return `<tool_call name="@commands" args='{"cmd":"list"}' />
<tool_call name="@commands" args='{"cmd":"get","args":{"name":"review-pr","args":"1326 security"}}' />

Lenient forms also accepted:
  {"cmd":"get","name":"review-pr","args":"1326"}
  list
  get review-pr 1326 security`
}

// Version is semver.
func (*BuiltinCommandsPlugin) Version() string { return "1.0.0" }

// Path is empty for builtins.
func (*BuiltinCommandsPlugin) Path() string { return "" }

// Schema exposes the structured description for the agent prompt builder.
func (*BuiltinCommandsPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON object {cmd, name, args}",
		"flags": []map[string]interface{}{
			{"name": "cmd", "type": "string", "required": true, "description": "Operation: list | get"},
			{"name": "name", "type": "string", "required": false, "description": "Command invocation name (get), e.g. review-pr or frontend:deploy"},
			{"name": "args", "type": "string", "required": false, "description": "Argument string interpolated into the template's $ARGUMENTS/$1..$9 (get)"},
		},
		"examples": []string{`{"cmd":"list"}`, `{"cmd":"get","args":{"name":"review-pr","args":"1326"}}`},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches to the adapter.
func (p *BuiltinCommandsPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — no incremental output.
func (p *BuiltinCommandsPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentCommandsAdapter()
	if adapter == nil {
		return "", errors.New("@commands: no command catalog wired in this session")
	}
	op, name, cmdArgs := parseCommandsArgs(strings.TrimSpace(strings.Join(args, " ")))
	switch op {
	case "", "list", "ls":
		return adapter.List(), nil
	case "get", "show", "expand", "run":
		if name == "" {
			return "", errors.New(`@commands get: missing command name — {"cmd":"get","args":{"name":"<command>","args":"..."}}`)
		}
		out, ok := adapter.Get(ctx, name, cmdArgs)
		if !ok {
			return "", errors.New("@commands get: unknown command " + name + " — call {\"cmd\":\"list\"} to see the catalog")
		}
		return out, nil
	default:
		return "", errors.New("@commands: unknown operation " + op + " (use list | get)")
	}
}

// parseCommandsArgs accepts the canonical JSON envelope and every lenient
// variant the models actually emit — strict parsing here just makes the AI
// fail and retry, burning a full turn.
func parseCommandsArgs(payload string) (op, name, cmdArgs string) {
	if payload == "" {
		return "list", "", ""
	}
	if strings.HasPrefix(payload, "{") {
		var envelope struct {
			Cmd  string          `json:"cmd"`
			Op   string          `json:"op"`
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		}
		if json.Unmarshal([]byte(payload), &envelope) == nil {
			op = strings.ToLower(strings.TrimSpace(envelope.Cmd))
			if op == "" {
				op = strings.ToLower(strings.TrimSpace(envelope.Op))
			}
			name = strings.TrimSpace(envelope.Name)
			if len(envelope.Args) > 0 {
				// args may be a plain string or the nested {"name","args"} object.
				var s string
				if json.Unmarshal(envelope.Args, &s) == nil {
					cmdArgs = s
				} else {
					var nested struct {
						Name string `json:"name"`
						Args string `json:"args"`
					}
					if json.Unmarshal(envelope.Args, &nested) == nil {
						if name == "" {
							name = strings.TrimSpace(nested.Name)
						}
						cmdArgs = nested.Args
					}
				}
			}
			return op, name, cmdArgs
		}
	}
	// Plain-text form: "get review-pr 1326 security" / "list".
	fields := strings.Fields(payload)
	op = strings.ToLower(fields[0])
	if len(fields) > 1 {
		name = fields[1]
	}
	if len(fields) > 2 {
		cmdArgs = strings.Join(fields[2:], " ")
	}
	return op, name, cmdArgs
}
