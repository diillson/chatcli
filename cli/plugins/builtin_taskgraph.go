/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * @taskgraph — builtin over the task-graph orchestrator: an approved plan
 * becomes a persisted DAG whose independent tasks run in parallel on squad
 * workers, each gated by a validation contract the ENGINE verifies (gate
 * commands + independent reviewer worker). The executor's self-report is
 * never the verdict.
 *
 * Same adapter seam as the other builtins: the cli package wires the live
 * engine/dispatcher (SetTaskGraphAdapter); the plugin stays a thin
 * dispatcher.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// TaskGraphAdapter is the surface @taskgraph needs from the live session.
// All methods return model-facing text.
type TaskGraphAdapter interface {
	// Plan validates and persists a graph plan, returning its run id.
	Plan(graphJSON string) (string, error)
	// Run executes a run (runID, or a fresh one when graphJSON is given),
	// streaming progress through onOutput, and returns the final report.
	Run(ctx context.Context, runID, graphJSON string, onOutput func(string)) (string, error)
	// Status reports the run's current state (latest run when id is empty).
	Status(runID string) (string, error)
	// Show reports one task in full detail.
	Show(runID, taskID string) (string, error)
	// Retry resets a failed task (and its blocked successors) and resumes.
	Retry(ctx context.Context, runID, taskID string, onOutput func(string)) (string, error)
	// Cancel stops the active run.
	Cancel() (string, error)
	// List enumerates persisted runs.
	List() (string, error)
}

// TaskGraphDashboarder is the OPTIONAL dashboard capability of a
// TaskGraphAdapter (kept out of the base interface so existing
// implementations stay valid). Dash serves the live browser dashboard and
// returns its URL, opening the browser when a TTY is present.
type TaskGraphDashboarder interface {
	Dash(runID string) (string, error)
}

// TaskGraphPruner is the OPTIONAL retention capability: remove persisted
// runs older than a retention ("30d", "72h", "all"); the active run is
// never removed.
type TaskGraphPruner interface {
	Prune(olderThan string) (string, error)
}

type taskGraphAdapterHolder struct{ a TaskGraphAdapter }

var taskGraphAdapterAtom atomic.Value // taskGraphAdapterHolder

// SetTaskGraphAdapter wires the live adapter. Called from the top-level cli
// package at startup; pass nil to detach.
func SetTaskGraphAdapter(a TaskGraphAdapter) {
	taskGraphAdapterAtom.Store(taskGraphAdapterHolder{a: a})
}

func currentTaskGraphAdapter() TaskGraphAdapter {
	v := taskGraphAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(taskGraphAdapterHolder)
	return h.a
}

// BuiltinTaskGraphPlugin implements the Plugin interface for @taskgraph.
type BuiltinTaskGraphPlugin struct{}

// NewBuiltinTaskGraphPlugin returns a ready-to-register plugin.
func NewBuiltinTaskGraphPlugin() *BuiltinTaskGraphPlugin { return &BuiltinTaskGraphPlugin{} }

// Name returns "@taskgraph".
func (*BuiltinTaskGraphPlugin) Name() string { return "@taskgraph" }

// Description surfaces the tool in the agent tool catalog (model-facing).
func (*BuiltinTaskGraphPlugin) Description() string {
	return "Execute an approved multi-task plan as a task graph (DAG): independent tasks run in parallel on squad workers, dependencies gate execution, and NO task counts as done on the executor's word — the engine runs each task's validation commands itself and a fresh reviewer worker issues the verdict (done or retry with feedback). Use it for deliveries of 5+ tasks with real independence between them; for less, dispatch <agent_call> workers directly."
}

// Usage explains the canonical invocation forms.
func (*BuiltinTaskGraphPlugin) Usage() string {
	return `@taskgraph plan|run|status|show|retry|cancel|list|dash|prune

<tool_call name="@taskgraph" args='{"cmd":"run","args":{"graph":{"name":"my-feature","tasks":[{"id":"T1","title":"...","prompt":"...","validation":[{"run":"go test ./...","expect":"all green"}]},{"id":"T2","prompt":"...","deps":["T1"]}]}}}' />

For real graphs write the plan JSON to a file first and pass {"cmd":"run","args":{"file":"/tmp/plan.json"}} — long inline args get truncated. run executes the WHOLE graph (streamed); status/show/list inspect; retry re-opens a failed task; cancel stops the active run; dash serves the live browser dashboard.`
}

// Version returns the plugin contract version.
func (*BuiltinTaskGraphPlugin) Version() string { return "1.0.0" }

// Path returns "" so the tool catalog defers the full definition to the
// index (the task-graph skill teaches the envelopes).
func (*BuiltinTaskGraphPlugin) Path() string { return "" }

// Schema declares the machine-readable command surface.
func (*BuiltinTaskGraphPlugin) Schema() string {
	graphDesc := `graph plan object: {"name","max_parallel"?,"require_review"? (default true),"phases"?:[{"id","title"}],"tasks":[{"id","title"?,"agent"? (default coder),"prompt","deps"?:[ids declared earlier],"phase"?,"validation"?:[{"run":shell cmd the ENGINE executes,"expect":prose}] or prose,"max_attempts"? (default 3),"require_review"?}]}`
	schema := map[string]interface{}{
		"name":        "@taskgraph",
		"description": "DAG executor for approved multi-task plans: parallel squad workers + engine-run validation gates + independent reviewer verdicts.",
		"argsFormat":  `JSON envelope {cmd, args} (e.g. {"cmd":"run","args":{"graph":{...}}})`,
		"subcommands": []map[string]interface{}{
			{
				"name":        "plan",
				"description": "Validate and persist a graph plan without executing (returns the run id).",
				"flags": []map[string]interface{}{
					{"name": "graph", "type": "object", "required": false, "description": graphDesc},
					{"name": "file", "type": "string", "required": false, "description": "path to a JSON file holding the plan — REQUIRED in practice for real graphs (long inline args get truncated); write the plan with @coder write first"},
				},
				"examples": []string{`{"cmd":"plan","args":{"file":"/tmp/plan.json"}}`, `{"cmd":"plan","args":{"graph":{"name":"feature-x","tasks":[{"id":"T1","prompt":"..."}]}}}`},
			},
			{
				"name":        "run",
				"description": "Execute a graph to completion (streams progress; the call returns the final report). Give a graph to plan+run in one call, or an id to run/resume a persisted plan (latest when omitted).",
				"flags": []map[string]interface{}{
					{"name": "file", "type": "string", "required": false, "description": "path to a JSON file holding the plan — the RELIABLE form for real graphs: write it with @coder write, then run with file (long inline args get truncated into 'unexpected end of JSON input')"},
					{"name": "graph", "type": "object", "required": false, "description": graphDesc + " (inline form — only for small graphs)"},
					{"name": "id", "type": "string", "required": false, "description": "run id from plan/list (default: latest)"},
				},
				"examples": []string{`{"cmd":"run","args":{"file":"/tmp/plan.json"}}`, `{"cmd":"run","args":{"id":"tg-20260829-153000"}}`},
			},
			{
				"name":        "status",
				"description": "Current state of a run: per-task status, attempts, verdicts, cost. Default subcommand.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": false, "description": "run id (default: latest)"},
				},
				"examples": []string{`{"cmd":"status"}`},
			},
			{
				"name":        "show",
				"description": "One task in full: prompt, contract, every attempt with gate outputs, reviewer verdicts and evidence.",
				"flags": []map[string]interface{}{
					{"name": "task", "type": "string", "required": true, "description": "task id, e.g. T3"},
					{"name": "id", "type": "string", "required": false, "description": "run id (default: latest)"},
				},
				"examples": []string{`{"cmd":"show","args":{"task":"T3"}}`},
			},
			{
				"name":        "retry",
				"description": "Re-open a failed task (unblocking its successors) and resume the run.",
				"flags": []map[string]interface{}{
					{"name": "task", "type": "string", "required": true, "description": "failed task id"},
					{"name": "id", "type": "string", "required": false, "description": "run id (default: latest)"},
				},
				"examples": []string{`{"cmd":"retry","args":{"task":"T3"}}`},
			},
			{
				"name":        "cancel",
				"description": "Stop the active run (tasks end when they observe the signal).",
				"examples":    []string{`{"cmd":"cancel"}`},
			},
			{
				"name":        "list",
				"description": "List persisted runs, newest first.",
				"examples":    []string{`{"cmd":"list"}`},
			},
			{
				"name":        "prune",
				"description": "Remove persisted runs older than a retention (default 30d; the active run is never removed). Runs also auto-prune at 30d.",
				"flags": []map[string]interface{}{
					{"name": "older_than", "type": "string", "required": false, "description": "retention: Go duration, Nd days, or all (default 30d)"},
				},
				"examples": []string{`{"cmd":"prune"}`, `{"cmd":"prune","args":{"older_than":"7d"}}`, `{"cmd":"prune","args":{"older_than":"all"}}`},
			},
			{
				"name":        "dash",
				"description": "Serve the live browser dashboard (animated DAG, swimlanes, per-task evidence and cost) and return its local URL.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": false, "description": "run id to focus (default: latest)"},
				},
				"examples": []string{`{"cmd":"dash"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute delegates to ExecuteWithStream.
func (p *BuiltinTaskGraphPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs one @taskgraph subcommand.
func (p *BuiltinTaskGraphPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	adapter := currentTaskGraphAdapter()
	if adapter == nil {
		return "", errors.New("@taskgraph: no adapter wired (task graph engine not available in this session)")
	}
	sub, payload, err := parseTaskGraphInvocation(args)
	if err != nil {
		return "", err
	}
	get := func(keys ...string) string { return strings.TrimSpace(jsonString(payload, keys...)) }
	graphJSON := taskGraphPlanJSON(payload)
	switch sub {
	case "plan", "create", "validate", "run", "start", "resume", "exec":
		if graphJSON == "" {
			if graphJSON, err = taskGraphPlanFromFile(get("file", "path", "graph_file", "graphFile", "plan_file")); err != nil {
				return "", err
			}
		}
	}

	switch sub {
	case "plan", "create", "validate":
		if graphJSON == "" {
			return "", errors.New(`@taskgraph plan: missing "graph" (the plan object). Example: {"cmd":"plan","args":{"graph":{"name":"x","tasks":[...]}}}`)
		}
		return adapter.Plan(graphJSON)
	case "run", "start", "resume", "exec":
		return adapter.Run(ctx, get("id", "run_id", "runId", "run"), graphJSON, onOutput)
	case "status", "state", "progress":
		return adapter.Status(get("id", "run_id", "runId", "run"))
	case "show", "task", "inspect", "get":
		task := get("task", "task_id", "taskId")
		if task == "" {
			return "", errors.New(`@taskgraph show: missing "task" (e.g. {"cmd":"show","args":{"task":"T3"}})`)
		}
		return adapter.Show(get("id", "run_id", "runId", "run"), task)
	case "retry", "reopen":
		task := get("task", "task_id", "taskId")
		if task == "" {
			return "", errors.New(`@taskgraph retry: missing "task" (e.g. {"cmd":"retry","args":{"task":"T3"}})`)
		}
		return adapter.Retry(ctx, get("id", "run_id", "runId", "run"), task, onOutput)
	case "cancel", "stop", "abort":
		return adapter.Cancel()
	case "list", "ls", "runs":
		return adapter.List()
	case "prune", "gc", "clean":
		pr, ok := adapter.(TaskGraphPruner)
		if !ok {
			return "", errors.New("@taskgraph prune: retention not available in this session")
		}
		return pr.Prune(get("older_than", "olderThan", "age", "retention"))
	case "dash", "dashboard", "ui":
		d, ok := adapter.(TaskGraphDashboarder)
		if !ok {
			return "", errors.New("@taskgraph dash: dashboard not available in this session")
		}
		return d.Dash(get("id", "run_id", "runId", "run"))
	default:
		return "", fmt.Errorf("@taskgraph: unknown subcommand %q (expected plan|run|status|show|retry|cancel|list|dash|prune)", sub)
	}
}

// taskGraphMaxPlanFileBytes bounds a plan file read.
const taskGraphMaxPlanFileBytes = 1 << 20

// taskGraphPlanFromFile loads a graph plan from a JSON file — the reliable
// path for real graphs: long inline args get truncated by output-token
// limits ("unexpected end of JSON input"), a file never does. The file may
// contain the bare graph, {"graph":{...}}, or a full {cmd,args} envelope.
func taskGraphPlanFromFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("@taskgraph: plan file %q: %w", path, err)
	}
	if info.Size() > taskGraphMaxPlanFileBytes {
		return "", fmt.Errorf("@taskgraph: plan file %q too large (%d bytes, max %d)", path, info.Size(), taskGraphMaxPlanFileBytes)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- explicit plan-file path requested by the tool call; size-capped above
	if err != nil {
		return "", fmt.Errorf("@taskgraph: read plan file %q: %w", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return "", fmt.Errorf("@taskgraph: plan file %q is not valid JSON: %w", path, err)
	}
	// Unwrap {cmd,args:{graph}} and {graph:{...}} shapes down to the plan.
	for range [2]struct{}{} {
		if inner, ok := top["args"]; ok {
			var next map[string]json.RawMessage
			if json.Unmarshal(inner, &next) == nil {
				top = next
				continue
			}
		}
		break
	}
	if g := taskGraphPlanJSON(top); g != "" {
		return g, nil
	}
	return "", fmt.Errorf("@taskgraph: plan file %q has no graph (expected a plan object with \"tasks\")", path)
}

// taskGraphPlanJSON extracts the graph plan object (aliases tolerated) as a
// raw JSON string, or "".
func taskGraphPlanJSON(payload map[string]json.RawMessage) string {
	for _, key := range []string{"graph", "plan", "tasks_graph", "dag"} {
		if v, ok := payload[key]; ok {
			s := strings.TrimSpace(string(v))
			if s != "" && s != "null" {
				// A string value carries JSON-in-a-string; unwrap it.
				if strings.HasPrefix(s, `"`) {
					var unwrapped string
					if json.Unmarshal(v, &unwrapped) == nil && strings.TrimSpace(unwrapped) != "" {
						return unwrapped
					}
				}
				return s
			}
		}
	}
	// A flat payload that IS the plan (has "tasks") counts as the graph.
	if v, ok := payload["tasks"]; ok && strings.TrimSpace(string(v)) != "" {
		if b, err := json.Marshal(payload); err == nil {
			return string(b)
		}
	}
	return ""
}

// parseTaskGraphInvocation resolves subcommand + payload from the canonical
// JSON envelope or the flattened/positional argv forms. No args = status.
func parseTaskGraphInvocation(args []string) (string, map[string]json.RawMessage, error) {
	if len(args) == 0 {
		return "status", map[string]json.RawMessage{}, nil
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		blob := strings.TrimSpace(strings.Join(args, " "))
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(blob), &top); err != nil {
			return "", nil, fmt.Errorf("@taskgraph: malformed JSON args: %w", err)
		}
		var sub string
		if cmd, ok := top["cmd"]; ok {
			_ = json.Unmarshal(cmd, &sub)
		}
		if sub == "" {
			// Flat native-tool-calling payload: a plan object means run,
			// anything else means status.
			if isFlatArgs(top) && taskGraphPlanJSON(top) != "" {
				sub = "run"
			} else {
				sub = "status"
			}
		}
		var inner map[string]json.RawMessage
		if rawInner, ok := top["args"]; ok {
			_ = json.Unmarshal(rawInner, &inner)
		}
		if inner == nil {
			inner = top
		}
		return strings.ToLower(sub), inner, nil
	}

	// argv fallback: "show T3", "run tg-...", "retry T3 --id tg-..." plus
	// the flattened --flag forms produced by the agent loop.
	sub := strings.ToLower(first)
	positional, innerJSON := argvToInnerJSON(args[1:], nil, nil)
	inner := map[string]json.RawMessage{}
	if innerJSON != "{}" {
		if err := json.Unmarshal([]byte(innerJSON), &inner); err != nil {
			inner = map[string]json.RawMessage{}
		}
	}
	setIfMissing := func(key, value string) {
		if value == "" {
			return
		}
		if _, ok := inner[key]; !ok {
			raw, _ := json.Marshal(value)
			inner[key] = raw
		}
	}
	switch sub {
	case "show", "task", "inspect", "get", "retry", "reopen":
		setIfMissing("task", positional)
	case "prune", "gc", "clean":
		setIfMissing("older_than", positional)
	default:
		setIfMissing("id", positional)
	}
	return sub, inner, nil
}
