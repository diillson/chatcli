/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * @mail — builtin tool over the squad message bus (orchestrator surface).
 *
 * Workers send mail through their native send_mail tool; the orchestrator
 * uses @mail to message workers (directives land on their next ReAct turn),
 * check its own inbox and audit recent traffic. The user's surface is the
 * /mail command.
 *
 * The plugin never imports cli/agent/mail directly — the cli package wires
 * a live adapter at boot (SetMailAdapter), same pattern as @todo/@agents.
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

// MailAdapter is the contract the cli package fulfills to expose the squad
// message bus to this plugin. The adapter fixes the sender identity to
// "orchestrator" — the plugin runs inside the orchestrator loop.
type MailAdapter interface {
	Send(to, cardID, text string) (string, error)
	Inbox() (string, error)
	History(n int) (string, error)
}

var (
	mailAdapterMu sync.RWMutex
	mailAdapter   MailAdapter
)

// SetMailAdapter installs (or clears, with nil) the live adapter.
func SetMailAdapter(a MailAdapter) {
	mailAdapterMu.Lock()
	defer mailAdapterMu.Unlock()
	mailAdapter = a
}

func currentMailAdapter() MailAdapter {
	mailAdapterMu.RLock()
	defer mailAdapterMu.RUnlock()
	return mailAdapter
}

// BuiltinMailPlugin implements the Plugin interface for @mail.
type BuiltinMailPlugin struct{}

// NewBuiltinMailPlugin creates the builtin @mail plugin.
func NewBuiltinMailPlugin() *BuiltinMailPlugin { return &BuiltinMailPlugin{} }

func (p *BuiltinMailPlugin) Name() string        { return "@mail" }
func (p *BuiltinMailPlugin) Description() string { return i18n.T("plugins.mail.description") }
func (p *BuiltinMailPlugin) Usage() string       { return "@mail send|inbox|history" }
func (p *BuiltinMailPlugin) Version() string     { return "1.0.0" }
func (p *BuiltinMailPlugin) Path() string        { return "[builtin]" }

// Schema documents the tool for the agent system prompt catalog.
func (p *BuiltinMailPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON",
		"subcommands": []map[string]interface{}{
			{
				"name":        "send",
				"description": "Send a directed message to a squad agent. It is injected into the recipient's context at its next turn.",
				"flags": []map[string]interface{}{
					{"name": "to", "type": "string", "required": true, "description": "recipient: worker agent type (coder, reviewer, …)"},
					{"name": "text", "type": "string", "required": true, "description": "the message — specific and actionable"},
					{"name": "card_id", "type": "string", "required": false, "description": "board card this message is about"},
				},
				"examples": []string{`{"cmd":"send","args":{"to":"coder","text":"review failed: missing tests for X","card_id":"card-2"}}`},
			},
			{
				"name":        "inbox",
				"description": "Drain your own (orchestrator) inbox now instead of waiting for the next turn boundary.",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"inbox"}`},
			},
			{
				"name":        "history",
				"description": "Show recent squad mail traffic (all senders/recipients), newest first.",
				"flags": []map[string]interface{}{
					{"name": "limit", "type": "integer", "required": false, "description": "max messages (default 20)"},
				},
				"examples": []string{`{"cmd":"history"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute delegates to ExecuteWithStream.
func (p *BuiltinMailPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs one @mail subcommand.
func (p *BuiltinMailPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentMailAdapter()
	if adapter == nil {
		return "", errors.New("@mail: no adapter wired (squad mail not available)")
	}

	sub, payload, err := parseMailInvocation(args)
	if err != nil {
		return "", err
	}
	get := func(keys ...string) string { return strings.TrimSpace(jsonString(payload, keys...)) }

	switch sub {
	case "send", "post":
		to := get("to", "recipient", "agent")
		text := get("text", "message", "body")
		if to == "" || text == "" {
			return "", errors.New(`@mail send: requires "to" and "text"`)
		}
		return adapter.Send(to, get("card_id", "cardId", "card"), text)
	case "inbox", "drain", "read":
		return adapter.Inbox()
	case "history", "recent", "log":
		limit := jsonInt(payload, "limit", "n", "count")
		if limit <= 0 {
			limit = 20
		}
		return adapter.History(limit)
	default:
		return "", fmt.Errorf("@mail: unknown subcommand %q (expected send|inbox|history)", sub)
	}
}

// parseMailInvocation resolves subcommand + payload from the canonical JSON
// envelope or a lenient argv form ("send coder fix the tests"). No args =
// inbox.
func parseMailInvocation(args []string) (string, map[string]json.RawMessage, error) {
	if len(args) == 0 {
		return "inbox", nil, nil
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(first), &top); err != nil {
			return "", nil, fmt.Errorf("@mail: malformed JSON args: %w", err)
		}
		var sub string
		if cmd, ok := top["cmd"]; ok {
			_ = json.Unmarshal(cmd, &sub)
		}
		if sub == "" {
			sub = "inbox"
		}
		var inner map[string]json.RawMessage
		if rawInner, ok := top["args"]; ok {
			_ = json.Unmarshal(rawInner, &inner)
		}
		if inner == nil {
			inner = top
		}
		return sub, inner, nil
	}

	// argv fallback: "send <to> <text...>".
	sub := first
	inner := map[string]json.RawMessage{}
	if sub == "send" && len(args) >= 3 {
		rawTo, _ := json.Marshal(args[1])
		rawText, _ := json.Marshal(strings.Join(args[2:], " "))
		inner["to"] = rawTo
		inner["text"] = rawText
	}
	return sub, inner, nil
}
