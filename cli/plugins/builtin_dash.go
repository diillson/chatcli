/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * BuiltinDashPlugin — the live telemetry dashboard as an @dash ReAct tool.
 *
 * Gives the model the same handle on the dashboard the user has with /dash
 * (start it and hand the address over, stop it, see who is recording) and,
 * on top of that, a way to READ what the dashboard sees: the reduced graph
 * of this process or of every chatcli process on the machine (agents, LLM
 * requests per model with cost, tools, skills, MCP servers, patterns,
 * background work, connections, recent errors) and the most recent raw
 * events, filterable by kind and status. It can also leave a short phase
 * label on the timeline.
 *
 * Like @model and @agents, the cli package owns the recorder and the server,
 * so the plugin reaches them through an adapter supplied via SetDashAdapter.
 * Everything the adapter returns is pre-rendered, model-facing text, and it
 * is metadata only: the telemetry never carries prompt or tool content.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// DashAdapter is the surface the @dash tool needs from the live session.
type DashAdapter interface {
	// Status reports whether this process records, the dashboard address
	// when one is served, and which processes on the machine report.
	Status(ctx context.Context) (string, error)
	// Open starts the dashboard (once per session), takes the recording
	// lease and returns the address; browser asks for the page to be opened
	// too, which only happens on an interactive terminal.
	Open(ctx context.Context, browser bool) (string, error)
	// Off stops the dashboard and lets the recording lease run out.
	Off(ctx context.Context) (string, error)
	// Summary renders the reduced graph of this process, or of every live
	// process when all is set.
	Summary(ctx context.Context, all bool) (string, error)
	// Events renders the most recent raw events matching the query.
	Events(ctx context.Context, q DashEventsQuery) (string, error)
	// Mark leaves a short phase label on this process's timeline.
	Mark(ctx context.Context, note string) (string, error)
}

// DashEventsQuery narrows an Events call.
type DashEventsQuery struct {
	Limit  int    // how many of the newest events; 0 = default
	Kind   string // one node kind (tool, llm, agent, ...) or ""
	Status string // one status (error, ok, running, ...) or ""
	All    bool   // every live process instead of this one
}

type dashAdapterHolder struct{ a DashAdapter }

var dashAdapterAtom atomic.Value // dashAdapterHolder

// SetDashAdapter wires the live adapter. Called from the top-level cli
// package at startup; pass nil to detach.
func SetDashAdapter(a DashAdapter) {
	dashAdapterAtom.Store(dashAdapterHolder{a: a})
}

func currentDashAdapter() DashAdapter {
	v := dashAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(dashAdapterHolder)
	return h.a
}

// BuiltinDashPlugin is the @dash tool.
type BuiltinDashPlugin struct{}

// NewBuiltinDashPlugin returns a ready-to-register plugin.
func NewBuiltinDashPlugin() *BuiltinDashPlugin { return &BuiltinDashPlugin{} }

// Name returns "@dash".
func (*BuiltinDashPlugin) Name() string { return "@dash" }

// Description surfaces the tool in the catalog and doubles as the policy
// for using it, so the guidance lives here instead of in the system prompt.
func (*BuiltinDashPlugin) Description() string {
	return "Manage the live telemetry dashboard (/dash) and read what it sees. " +
		"status: whether this process is recording, the dashboard address if one is served, and which chatcli processes report. " +
		"open: start the dashboard and return its address to hand to the user (the browser opens too on an interactive terminal); url: the same without the browser; off: stop it. " +
		"summary: what this process (or every process, all=true) has done since recording started — session model, route and cost, agents, LLM requests per model with tokens and cost, tools, skills, MCP servers, harness patterns, background work, connections and recent errors — the numbers the dashboard shows, as text. " +
		"events: the newest raw events (metadata only), filterable by kind and status. " +
		"mark: leave a short phase label on the timeline. " +
		"Recording is on only while a dashboard is open or CHATCLI_DASH=1; summary and events say so when it is off, and open turns it on. " +
		"Use summary or events to answer what is running, what failed and what a run cost; never poll them in a loop."
}

// Usage explains the canonical invocation forms.
func (*BuiltinDashPlugin) Usage() string {
	return `<tool_call name="@dash" args='{"cmd":"status"}' />
<tool_call name="@dash" args='{"cmd":"open"}' />
<tool_call name="@dash" args='{"cmd":"summary","args":{"all":true}}' />
<tool_call name="@dash" args='{"cmd":"events","args":{"kind":"tool","status":"error","limit":20}}' />
<tool_call name="@dash" args='{"cmd":"mark","args":{"note":"phase: tests"}}' />

Subcommands (cmd + args):
  status                     recording state, dashboard address, processes reporting
  open                       start the dashboard, take the recording lease, return the address (browser on a terminal)
  url                        same as open, never opens a browser
  off                        stop the dashboard; processes go quiet as the lease runs out
  summary {all?}             the reduced graph of this process (all=true: every live process)
  events {limit?, kind?, status?, all?}
                             newest raw events, metadata only (default 30, max 200)
  mark {note}                a short phase label on this process's timeline (up to 80 characters)`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinDashPlugin) Version() string { return "1.0.0" }

// Path is "[builtin]" for builtin plugins.
func (*BuiltinDashPlugin) Path() string { return "[builtin]" }

// Schema exposes the structured description the agent prompt builder renders
// into per-subcommand flag lists with examples.
func (*BuiltinDashPlugin) Schema() string {
	allFlag := map[string]interface{}{"name": "all", "description": "every live chatcli process on the machine instead of this one", "type": "boolean", "required": false}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "status",
				"description": "whether this process records, the dashboard address if served, and which processes report; the default subcommand",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"status"}`},
			},
			{
				"name":        "open",
				"description": "start the live dashboard, take the recording lease and return its address for the user; the browser opens too on an interactive terminal",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"open"}`},
			},
			{
				"name":        "url",
				"description": "start the dashboard if needed and return its address without opening a browser",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"url"}`},
			},
			{
				"name":        "off",
				"description": "stop the dashboard served by this process; recording stops as the lease runs out",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"off"}`},
			},
			{
				"name":        "summary",
				"description": "the reduced graph since recording started: session model/route/cost, agents, LLM requests per model with tokens and cost, tools, skills, MCP servers, patterns, background work, connections, recent errors",
				"flags":       []map[string]interface{}{allFlag},
				"examples":    []string{`{"cmd":"summary"}`, `{"cmd":"summary","args":{"all":true}}`},
			},
			{
				"name":        "events",
				"description": "the newest raw telemetry events, metadata only, optionally narrowed to one kind and one status",
				"flags": []map[string]interface{}{
					{"name": "limit", "description": "how many of the newest events (default 30, max 200)", "type": "number", "required": false},
					{"name": "kind", "description": "one node kind: session, agent, turn, llm, tool, skill, mcp, pattern, background, conn, rpc", "type": "string", "required": false},
					{"name": "status", "description": "one status: running, ok, error, cancelled, blocked", "type": "string", "required": false},
					allFlag,
				},
				"examples": []string{`{"cmd":"events","args":{"limit":20}}`, `{"cmd":"events","args":{"kind":"tool","status":"error"}}`},
			},
			{
				"name":        "mark",
				"description": "leave a short phase label on this process's timeline, visible in the dashboard feed",
				"flags": []map[string]interface{}{
					{"name": "note", "description": "the label, one line, up to 80 characters (a phase name, never file or user content)", "type": "string", "required": true},
				},
				"examples": []string{`{"cmd":"mark","args":{"note":"phase: running the test suite"}}`},
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// Execute implements the plugin contract.
func (p *BuiltinDashPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream dispatches to the adapter. This tool produces no
// incremental output, so the stream callback is ignored.
func (p *BuiltinDashPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentDashAdapter()
	if adapter == nil {
		return "", errors.New("@dash: the live dashboard is not available in this session")
	}
	inv, err := parseDashInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@dash: %w", err)
	}
	switch inv.cmd {
	case "status":
		return adapter.Status(ctx)
	case "open":
		return adapter.Open(ctx, true)
	case "url":
		return adapter.Open(ctx, false)
	case "off":
		return adapter.Off(ctx)
	case "summary":
		return adapter.Summary(ctx, inv.all)
	case "events":
		return adapter.Events(ctx, DashEventsQuery{Limit: inv.limit, Kind: inv.kind, Status: inv.status, All: inv.all})
	case "mark":
		if strings.TrimSpace(inv.note) == "" {
			return "", errors.New(`@dash: mark requires "note". Example: {"cmd":"mark","args":{"note":"phase: tests"}}`)
		}
		return adapter.Mark(ctx, inv.note)
	default:
		return "", fmt.Errorf("@dash: unknown cmd %q (valid: status|open|url|off|summary|events|mark)", inv.cmd)
	}
}

// dashInvocation is the normalized result of parseDashInvocation.
type dashInvocation struct {
	cmd    string
	all    bool
	limit  int
	kind   string
	status string
	note   string
}

// parseDashInvocation extracts the subcommand and fields from any of the
// shapes the agent may produce. Lenient on purpose — strict parsing here
// only makes the model retry blindly:
//
//   - JSON envelope:  {"cmd":"events","args":{"kind":"tool","limit":20}}
//   - flat JSON:      {"cmd":"mark","note":"phase: tests"}
//   - positional:     summary all | events 20 | mark phase: tests
//   - flag form:      events --kind tool --status error --limit 20
//
// The flag form matters because the agent's tool flattener rewrites a
// {cmd,args} envelope into "--flag value" pairs, and a multi-word note
// arrives as ONE argv element that must not be re-split.
func parseDashInvocation(args []string) (dashInvocation, error) {
	inv := dashInvocation{}
	payload := strings.TrimSpace(strings.Join(args, " "))
	if payload == "" {
		inv.cmd = "status"
		return inv, nil
	}
	if strings.HasPrefix(payload, "{") {
		return parseDashJSON(payload)
	}

	var fields []string
	if len(args) > 1 {
		for _, a := range args {
			if t := strings.TrimSpace(a); t != "" {
				fields = append(fields, t)
			}
		}
	} else {
		fields = splitArgsRespectingQuotes(payload)
	}
	var positionals []string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if !strings.HasPrefix(f, "-") {
			positionals = append(positionals, trimQuotes(f))
			continue
		}
		key, val, hasEq := strings.Cut(f, "=")
		key = strings.ToLower(strings.TrimLeft(key, "-"))
		grab := func() string {
			if hasEq {
				return trimQuotes(val)
			}
			if i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "-") {
				i++
				return trimQuotes(fields[i])
			}
			return ""
		}
		switch key {
		case "all", "everything", "every":
			v := grab()
			inv.all = v == "" || parseDashBool(v)
		case "limit", "n", "count", "last":
			inv.limit = parseDashInt(grab())
		case "kind", "type":
			inv.kind = strings.ToLower(grab())
		case "status", "state":
			inv.status = strings.ToLower(grab())
		case "note", "label", "text", "message", "msg":
			inv.note = grab()
		case "cmd", "command", "action":
			v := grab()
			if inv.cmd = canonicalDashCmd(v); inv.cmd == "" {
				inv.cmd = strings.ToLower(strings.TrimSpace(v))
			}
		}
	}
	for idx, p := range positionals {
		if inv.cmd == "" {
			if c := canonicalDashCmd(p); c != "" {
				inv.cmd = c
				continue
			}
		}
		switch inv.cmd {
		case "mark":
			// Everything after the subcommand is the note.
			inv.note = firstNonEmptyStr(inv.note, strings.Join(positionals[idx:], " "))
		case "summary", "events":
			if strings.EqualFold(p, "all") {
				inv.all = true
			} else if n := parseDashInt(p); n > 0 {
				inv.limit = n
			} else if inv.kind == "" {
				inv.kind = strings.ToLower(p)
			}
		}
		if inv.cmd == "mark" {
			break
		}
	}
	if inv.cmd == "" {
		inv.cmd = "status"
	}
	return inv, nil
}

func parseDashJSON(payload string) (dashInvocation, error) {
	inv := dashInvocation{}
	var raw struct {
		Cmd    string          `json:"cmd"`
		Args   json.RawMessage `json:"args"`
		All    json.RawMessage `json:"all"`
		Scope  string          `json:"scope"`
		Limit  json.RawMessage `json:"limit"`
		Kind   string          `json:"kind"`
		Status string          `json:"status"`
		Note   string          `json:"note"`
		Label  string          `json:"label"`
		Text   string          `json:"text"`
	}
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return inv, fmt.Errorf(`parse envelope: %w. Expected {"cmd":"summary","args":{"all":true}}`, err)
	}
	apply := func(all, limit json.RawMessage, scope, kind, status, note string) {
		if len(all) > 0 {
			inv.all = inv.all || parseDashBool(strings.Trim(string(all), `"`))
		}
		if strings.EqualFold(strings.TrimSpace(scope), "all") {
			inv.all = true
		}
		if len(limit) > 0 {
			if n := parseDashInt(strings.Trim(string(limit), `"`)); n > 0 {
				inv.limit = n
			}
		}
		inv.kind = firstNonEmptyStr(strings.ToLower(kind), inv.kind)
		inv.status = firstNonEmptyStr(strings.ToLower(status), inv.status)
		inv.note = firstNonEmptyStr(note, inv.note)
	}
	apply(raw.All, raw.Limit, raw.Scope, raw.Kind, raw.Status, firstNonEmptyStr(raw.Note, raw.Label, raw.Text))
	if len(raw.Args) > 0 {
		var nested struct {
			All    json.RawMessage `json:"all"`
			Scope  string          `json:"scope"`
			Limit  json.RawMessage `json:"limit"`
			Kind   string          `json:"kind"`
			Status string          `json:"status"`
			Note   string          `json:"note"`
			Label  string          `json:"label"`
			Text   string          `json:"text"`
		}
		if err := json.Unmarshal(raw.Args, &nested); err == nil {
			apply(nested.All, nested.Limit, nested.Scope, nested.Kind, nested.Status, firstNonEmptyStr(nested.Note, nested.Label, nested.Text))
		}
	}
	inv.cmd = canonicalDashCmd(raw.Cmd)
	if inv.cmd == "" {
		switch {
		case strings.TrimSpace(raw.Cmd) != "":
			// An unknown cmd is reported as such, never silently run as status.
			inv.cmd = strings.ToLower(strings.TrimSpace(raw.Cmd))
		case inv.note != "":
			inv.cmd = "mark"
		default:
			inv.cmd = "status"
		}
	}
	return inv, nil
}

func parseDashBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "all":
		return true
	}
	return false
}

func parseDashInt(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func canonicalDashCmd(cmd string) string {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "status", "st", "state", "info":
		return "status"
	case "open", "on", "start", "show", "ui":
		return "open"
	case "url", "link", "address":
		return "url"
	case "off", "stop", "close", "shutdown":
		return "off"
	case "summary", "graph", "overview", "nodes", "report":
		return "summary"
	case "events", "feed", "log", "tail", "recent":
		return "events"
	case "mark", "note", "annotate", "label", "phase":
		return "mark"
	default:
		return ""
	}
}
