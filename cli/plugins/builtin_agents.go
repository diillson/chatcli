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
	"strings"
	"sync"

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
func (p *BuiltinAgentsPlugin) Usage() string       { return "@agents list|show|cancel" }
func (p *BuiltinAgentsPlugin) Version() string     { return "1.0.0" }
func (p *BuiltinAgentsPlugin) Path() string        { return "[builtin]" }

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
func (p *BuiltinAgentsPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
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
	default:
		return "", fmt.Errorf("@agents: unknown subcommand %q (expected list|show|cancel)", sub)
	}
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

	// argv fallback: "show run-3" or "cancel --id run-3".
	positional, innerJSON := argvToInnerJSON(args[1:], nil, nil)
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
