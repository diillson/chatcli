/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * @agents — builtin tool over the live agent run registry.
 *
 * Gives the orchestrator LLM (and workers) eyes on the squad: which agent
 * runs are alive right now, what turn/action each one is at, the
 * parent/child tree, and the recent history — plus targeted cancellation.
 * This is the observability half of autonomous orchestration: the model
 * dispatches workers via <agent_call>, then uses @agents to watch and
 * manage them without human babysitting.
 *
 * The plugin never imports cli/agent/runs directly — the cli package wires
 * a live adapter at boot (SetAgentsAdapter), same pattern as @todo.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// AgentsAdapter is the contract the cli package fulfills to expose the live
// agent run registry to this plugin.
type AgentsAdapter interface {
	// List returns a compact, machine-readable listing of active runs
	// (with live turn/action) followed by recent finished runs.
	List() (string, error)
	// Show returns the detail of one run (live or finished) by ID.
	Show(id string) (string, error)
	// Cancel requests cancellation of a live run by ID.
	Cancel(id string) (string, error)
}

// AgentsWatcher is the optional extension an AgentsAdapter may implement to
// block on a run and to read its activity log. The plugin type-asserts it
// at call time so older adapters keep working.
type AgentsWatcher interface {
	// Wait blocks until the run reaches a terminal status, ctx ends or the
	// timeout elapses, and returns the run's final detail (or its current
	// state when the wait ran out).
	Wait(ctx context.Context, id string, timeout time.Duration) (string, error)
	// Transcript returns the last tail lines of the run's activity log.
	Transcript(id string, tail int) (string, error)
}

// Bounds for @agents wait and transcript.
const (
	agentsWaitDefault     = 2 * time.Minute
	agentsWaitMax         = 30 * time.Minute
	agentsTranscriptTail  = 200
	agentsTranscriptLimit = 2000
)

var (
	agentsAdapterMu sync.RWMutex
	agentsAdapter   AgentsAdapter
)

// SetAgentsAdapter installs (or clears, with nil) the live adapter.
func SetAgentsAdapter(a AgentsAdapter) {
	agentsAdapterMu.Lock()
	defer agentsAdapterMu.Unlock()
	agentsAdapter = a
}

func currentAgentsAdapter() AgentsAdapter {
	agentsAdapterMu.RLock()
	defer agentsAdapterMu.RUnlock()
	return agentsAdapter
}

// BuiltinAgentsPlugin implements the Plugin interface for @agents.
type BuiltinAgentsPlugin struct{}

// NewBuiltinAgentsPlugin creates the builtin @agents plugin.
func NewBuiltinAgentsPlugin() *BuiltinAgentsPlugin { return &BuiltinAgentsPlugin{} }

func (p *BuiltinAgentsPlugin) Name() string        { return "@agents" }
func (p *BuiltinAgentsPlugin) Description() string { return i18n.T("plugins.agents.description") }
func (p *BuiltinAgentsPlugin) Usage() string {
	return "@agents list|show|wait|transcript|cancel"
}
func (p *BuiltinAgentsPlugin) Version() string { return "1.2.0" }
func (p *BuiltinAgentsPlugin) Path() string    { return "[builtin]" }

// Schema documents the tool for the agent system prompt catalog.
func (p *BuiltinAgentsPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON",
		"subcommands": []map[string]interface{}{
			{
				"name":        "list",
				"description": "List live agent runs (kind, agent, status, current turn, current action, parent) plus recent finished runs. Default subcommand.",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"list"}`},
			},
			{
				"name":        "show",
				"description": "Show full detail of one run (live or finished) by run ID.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "run ID, e.g. run-3"},
				},
				"examples": []string{`{"cmd":"show","args":{"id":"run-3"}}`},
			},
			{
				"name":        "wait",
				"description": "Block until a run finishes (completed, failed or cancelled) and return its final status and detail. Bounded: default 2m, max 30m; when the timeout elapses the run's current state is returned and you may wait again.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "run ID, e.g. run-3"},
					{"name": "timeout", "type": "string", "required": false, "description": "Go duration (default 2m, max 30m); a bare number means seconds"},
				},
				"examples": []string{`{"cmd":"wait","args":{"id":"run-3"}}`, `{"cmd":"wait","args":{"id":"run-3","timeout":"10m"}}`, `wait run-3 --timeout 5m`},
			},
			{
				"name":        "transcript",
				"description": "Last N lines of a run's activity log: turn changes, action labels, the final status and an excerpt of a worker's final output. The runtime keeps this log, not the run's LLM conversation. Runs of other processes (via the hub) expose the newest 100 entries their owner mirrored.",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "run ID, e.g. run-3"},
					{"name": "tail", "type": "integer", "required": false, "description": "lines from the end (default 200, max 2000)"},
				},
				"examples": []string{`{"cmd":"transcript","args":{"id":"run-3"}}`, `{"cmd":"transcript","args":{"id":"run-3","tail":50}}`, `transcript run-3 --tail 50`},
			},
			{
				"name":        "cancel",
				"description": "Request cancellation of a live run (and everything it spawned).",
				"flags": []map[string]interface{}{
					{"name": "id", "type": "string", "required": true, "description": "run ID, e.g. run-3"},
				},
				"examples": []string{`{"cmd":"cancel","args":{"id":"run-3"}}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute delegates to ExecuteWithStream.
func (p *BuiltinAgentsPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs one @agents subcommand.
func (p *BuiltinAgentsPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentAgentsAdapter()
	if adapter == nil {
		return "", errors.New("@agents: no adapter wired (agent run registry not available)")
	}

	sub, payload, err := parseAgentsInvocation(args)
	if err != nil {
		return "", err
	}

	switch sub {
	case "list", "ls", "status":
		return adapter.List()
	case "show", "get", "info":
		id := agentsRunID(payload)
		if id == "" {
			return "", errors.New(`@agents show: missing "id" (e.g. {"cmd":"show","args":{"id":"run-3"}})`)
		}
		return adapter.Show(id)
	case "cancel", "stop", "kill":
		id := agentsRunID(payload)
		if id == "" {
			return "", errors.New(`@agents cancel: missing "id" (e.g. {"cmd":"cancel","args":{"id":"run-3"}})`)
		}
		return adapter.Cancel(id)
	case "wait", "join", "await":
		watcher, ok := adapter.(AgentsWatcher)
		if !ok {
			return "", errors.New(agentsMsgWatcherUnsupported)
		}
		id := agentsRunID(payload)
		if id == "" {
			return "", errors.New(`@agents wait: missing "id" (e.g. {"cmd":"wait","args":{"id":"run-3","timeout":"5m"}})`)
		}
		rawTimeout := strings.TrimSpace(jsonString(payload, "timeout", "for", "max"))
		if rawTimeout == "" {
			if secs := jsonInt(payload, "timeout", "for", "max"); secs > 0 {
				rawTimeout = strconv.Itoa(secs) // bare JSON number = seconds
			}
		}
		timeout, err := agentsWaitTimeout(rawTimeout)
		if err != nil {
			return "", err
		}
		return watcher.Wait(ctx, id, timeout)
	case "transcript", "log", "activity", "tail":
		watcher, ok := adapter.(AgentsWatcher)
		if !ok {
			return "", errors.New(agentsMsgWatcherUnsupported)
		}
		id := agentsRunID(payload)
		if id == "" {
			return "", errors.New(`@agents transcript: missing "id" (e.g. {"cmd":"transcript","args":{"id":"run-3","tail":50}})`)
		}
		tail := jsonInt(payload, "tail", "lines", "n", "limit")
		if tail <= 0 {
			tail = agentsTranscriptTail
		}
		if tail > agentsTranscriptLimit {
			tail = agentsTranscriptLimit
		}
		return watcher.Transcript(id, tail)
	default:
		return "", fmt.Errorf("@agents: unknown subcommand %q (expected list|show|wait|transcript|cancel)", sub)
	}
}

// agentsMsgWatcherUnsupported is returned when the wired adapter predates
// the wait/transcript extension.
const agentsMsgWatcherUnsupported = "@agents: wait and transcript are not supported by the wired agents adapter"

// agentsWaitTimeout parses the wait bound: empty = default, a Go duration,
// or a bare number of seconds. The result is clamped to [1s, max].
func agentsWaitTimeout(s string) (time.Duration, error) {
	if s == "" {
		return agentsWaitDefault, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		secs, convErr := strconv.Atoi(s)
		if convErr != nil {
			return 0, fmt.Errorf("@agents wait: invalid timeout %q (use a Go duration like 5m or seconds)", s)
		}
		d = time.Duration(secs) * time.Second
	}
	if d < time.Second {
		d = time.Second
	}
	if d > agentsWaitMax {
		d = agentsWaitMax
	}
	return d, nil
}

// agentsRunID extracts the run ID from the payload, tolerating the common
// aliases models reach for.
func agentsRunID(payload map[string]json.RawMessage) string {
	return strings.TrimSpace(jsonString(payload, "id", "run_id", "runId", "run"))
}

// parseAgentsInvocation resolves the subcommand + payload from either the
// canonical JSON envelope ({"cmd":"show","args":{"id":"run-3"}}) or a
// lenient argv form ("show run-3", "cancel --id run-3"). No args = list.
func parseAgentsInvocation(args []string) (string, map[string]json.RawMessage, error) {
	if len(args) == 0 {
		return "list", nil, nil
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(first), &top); err != nil {
			return "", nil, fmt.Errorf("@agents: malformed JSON args: %w", err)
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
		// Tolerate flat envelopes: {"cmd":"show","id":"run-3"}.
		if inner == nil {
			inner = top
		}
		return sub, inner, nil
	}

	// argv fallback: "show run-3", "cancel --id run-3", "wait run-3
	// --timeout 5m", "transcript run-3 --tail 50".
	positional, innerJSON := argvToInnerJSON(args[1:], nil, map[string]bool{"tail": true, "lines": true, "n": true, "limit": true})
	inner := map[string]json.RawMessage{}
	if innerJSON != "{}" {
		if err := json.Unmarshal([]byte(innerJSON), &inner); err != nil {
			inner = map[string]json.RawMessage{}
		}
	}
	if _, ok := inner["id"]; !ok && positional != "" {
		raw, _ := json.Marshal(positional)
		inner["id"] = raw
	}
	return first, inner, nil
}
