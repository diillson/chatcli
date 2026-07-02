/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * @proc — background-process supervision for the agent. The coder engine's
 * exec is one-shot; @proc owns the "start the dev server, keep it running,
 * read its logs, stop it" lifecycle behind real development verification.
 * Same adapter seam as the other builtins; the cli package wires it over a
 * session supervisor that shares the agent exec's command validator and
 * kills everything at session end.
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

// ProcAdapter is the surface @proc needs from the live session. Results are
// pre-rendered, model-facing text.
type ProcAdapter interface {
	Start(command, dir string) (string, error)
	Status(id string) (string, error)
	Logs(id string, tail int) (string, error)
	Stop(id string) (string, error)
	Remove(id string) (string, error)
	List() (string, error)
}

type procAdapterHolder struct{ a ProcAdapter }

var procAdapterAtom atomic.Value // procAdapterHolder

// SetProcAdapter wires the live adapter. Called from the top-level cli
// package at startup; pass nil to detach.
func SetProcAdapter(a ProcAdapter) {
	procAdapterAtom.Store(procAdapterHolder{a: a})
}

func currentProcAdapter() ProcAdapter {
	v := procAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(procAdapterHolder)
	return h.a
}

// BuiltinProcPlugin is the @proc tool.
type BuiltinProcPlugin struct{}

// NewBuiltinProcPlugin returns a ready-to-register plugin.
func NewBuiltinProcPlugin() *BuiltinProcPlugin { return &BuiltinProcPlugin{} }

// Name returns "@proc".
func (*BuiltinProcPlugin) Name() string { return "@proc" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinProcPlugin) Description() string {
	return "Run and supervise background processes: start a long-running command (dev server, watcher), check its status, tail its logs, and stop the whole process tree. Use it when a command must KEEP RUNNING while you test against it — one-shot commands belong to @coder exec."
}

// Usage explains the canonical invocation forms.
func (*BuiltinProcPlugin) Usage() string {
	return `<tool_call name="@proc" args='{"cmd":"start","args":{"command":"npm run dev","dir":"./web"}}' />

Subcommands (cmd + args):
  start  {command, dir?}      launch a background process, returns its id
  status {id}                 running/exited, pid, uptime, exit code
  logs   {id, tail?:1-1000}   last lines of combined stdout+stderr
  stop   {id}                 terminate the process tree (TERM → grace → KILL)
  remove {id}                 forget an exited process and its logs
  list   {}                   every tracked process

Workflow: start the server, poll logs until it is ready, exercise it (e.g.
@coder exec curl), read logs again, stop it when done. Everything still
running dies with the session.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinProcPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinProcPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders.
func (*BuiltinProcPlugin) Schema() string {
	idFlag := map[string]interface{}{"name": "id", "description": "process id returned by start (e.g. p1)", "type": "string", "required": true}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "start",
				"description": "launch a background process in its own process group",
				"flags": []map[string]interface{}{
					{"name": "command", "description": "shell command to run", "type": "string", "required": true},
					{"name": "dir", "description": "working directory (default: current)", "type": "string"},
				},
				"examples": []string{`{"cmd":"start","args":{"command":"go run ./cmd/server","dir":"."}}`},
			},
			{
				"name":        "status",
				"description": "state, pid, uptime and exit code of one process",
				"flags":       []map[string]interface{}{idFlag},
				"examples":    []string{`{"cmd":"status","args":{"id":"p1"}}`},
			},
			{
				"name":        "logs",
				"description": "tail of the combined stdout+stderr",
				"flags": []map[string]interface{}{idFlag,
					{"name": "tail", "description": "lines to return", "type": "int", "default": "100"},
				},
				"examples": []string{`{"cmd":"logs","args":{"id":"p1","tail":50}}`},
			},
			{
				"name":        "stop",
				"description": "terminate the whole process tree",
				"flags":       []map[string]interface{}{idFlag},
				"examples":    []string{`{"cmd":"stop","args":{"id":"p1"}}`},
			},
			{
				"name":        "remove",
				"description": "forget an exited process and its logs",
				"flags":       []map[string]interface{}{idFlag},
				"examples":    []string{`{"cmd":"remove","args":{"id":"p1"}}`},
			},
			{
				"name":        "list",
				"description": "every tracked process",
				"examples":    []string{`{"cmd":"list"}`},
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// Execute implements the plugin contract.
func (p *BuiltinProcPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — no incremental output.
func (p *BuiltinProcPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentProcAdapter()
	if adapter == nil {
		return "", errors.New("@proc: no process adapter wired in this session")
	}
	if len(args) == 0 {
		return "", errors.New(`@proc: empty args. Example: <tool_call name="@proc" args='{"cmd":"list"}' />`)
	}

	cmd, inner, err := parseProcInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@proc: %w", err)
	}

	var in struct {
		Command string `json:"command"`
		Dir     string `json:"dir"`
		ID      string `json:"id"`
		Tail    int    `json:"tail"`
	}
	_ = json.Unmarshal([]byte(inner), &in)

	switch cmd {
	case "start":
		if strings.TrimSpace(in.Command) == "" {
			return "", errors.New(`@proc start: "command" is required`)
		}
		return adapter.Start(in.Command, in.Dir)
	case "status":
		if err := requireProcID(in.ID); err != nil {
			return "", err
		}
		return adapter.Status(in.ID)
	case "logs":
		if err := requireProcID(in.ID); err != nil {
			return "", err
		}
		return adapter.Logs(in.ID, in.Tail)
	case "stop":
		if err := requireProcID(in.ID); err != nil {
			return "", err
		}
		return adapter.Stop(in.ID)
	case "remove":
		if err := requireProcID(in.ID); err != nil {
			return "", err
		}
		return adapter.Remove(in.ID)
	case "list":
		return adapter.List()
	default:
		return "", fmt.Errorf("@proc: unknown cmd %q (valid: start|status|logs|stop|remove|list)", cmd)
	}
}

func requireProcID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New(`@proc: "id" is required (see @proc list)`)
	}
	return nil
}

// procInvocationCmd extracts just the canonical subcommand (used by the caps
// layer to classify read-only vs mutating calls).
func procInvocationCmd(args []string) string {
	cmd, _, err := parseProcInvocation(args)
	if err != nil {
		return ""
	}
	return cmd
}

// parseProcInvocation accepts the JSON envelope {cmd, args} or the argv form
// ("logs p1" / "start 'npm run dev'").
func parseProcInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(`parse envelope: %w. Expected {"cmd":"start","args":{"command":"..."}}`, err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalProcCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: start|status|logs|stop|remove|list)", cmdStr)
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

	canon := canonicalProcCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("unknown cmd %q (valid: start|status|logs|stop|remove|list)", args[0])
	}
	in := map[string]interface{}{}
	if len(args) > 1 {
		if canon == "start" {
			in["command"] = strings.Join(args[1:], " ")
		} else {
			in["id"] = args[1]
		}
	}
	b, _ := json.Marshal(in)
	return canon, string(b), nil
}

// canonicalProcCmd folds aliases onto the canonical subcommand names.
func canonicalProcCmd(cmd string) string {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "start", "run", "spawn", "launch":
		return "start"
	case "status", "stat", "info":
		return "status"
	case "logs", "log", "tail", "output":
		return "logs"
	case "stop", "kill", "terminate":
		return "stop"
	case "remove", "rm", "forget", "clean":
		return "remove"
	case "list", "ls", "ps":
		return "list"
	default:
		return ""
	}
}
