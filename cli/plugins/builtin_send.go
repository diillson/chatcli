/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinSendPlugin — exposes proactive outbound messaging as an @send ReAct
 * tool. The agent can deliver a message to any configured gateway platform
 * (Telegram, WhatsApp, Discord, Slack, generic webhook) — the same adapters
 * the gateway daemon uses for replies, now reachable from agent/coder to
 * INITIATE a message. This is the chatcli equivalent of hermes-agent's
 * send_message tool.
 *
 * Like @memory/@scheduler, the cli package owns the gateway adapters but the
 * plugin is instantiated before it, so the plugin reaches them through a
 * package-level adapter supplied via SetSendAdapter.
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

// SendAdapter is the interface the BuiltinSendPlugin uses to deliver messages
// through the live gateway adapters. The chatcli top-level package provides an
// implementation bound to the configured platforms.
type SendAdapter interface {
	// Send delivers message to target. target is "platform" (home channel) or
	// "platform:chat_id" (e.g. "telegram:-100123", "whatsapp:+5511999999999").
	// Returns a short human/JSON result describing the outcome.
	Send(ctx context.Context, target, message string) (string, error)
	// List returns the configured platforms and any default targets.
	List(ctx context.Context) (string, error)
}

type sendAdapterHolder struct{ a SendAdapter }

var sendAdapterAtom atomic.Value // stores sendAdapterHolder

// SetSendAdapter wires the live adapter. Called from the top-level cli package
// once the gateway platform registry is available. Pass nil to clear it.
func SetSendAdapter(a SendAdapter) {
	sendAdapterAtom.Store(sendAdapterHolder{a: a})
}

func currentSendAdapter() SendAdapter {
	v := sendAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(sendAdapterHolder)
	return h.a
}

// BuiltinSendPlugin is the @send tool.
type BuiltinSendPlugin struct{}

// NewBuiltinSendPlugin returns a ready-to-register plugin.
func NewBuiltinSendPlugin() *BuiltinSendPlugin { return &BuiltinSendPlugin{} }

// Name returns "@send".
func (*BuiltinSendPlugin) Name() string { return "@send" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinSendPlugin) Description() string {
	return "Send a message to a connected messaging platform (Telegram, WhatsApp, Discord, Slack, webhook), or list the configured targets. Use it to proactively notify the user or deliver a result to a chat."
}

// Usage explains the canonical invocation forms.
func (*BuiltinSendPlugin) Usage() string {
	return `<tool_call name="@send" args='{"cmd":"send","args":{"to":"telegram:-1001234567890","message":"Build is green ✅"}}' />

Subcommands (cmd + args):
  send {to, message}   to = "platform" (default/home channel) or "platform:chat_id"
                       platforms: telegram, whatsapp, discord, slack, webhook
                       examples: "telegram", "telegram:-100123:17", "whatsapp:+5511999999999"
  list                 list configured platforms and default targets`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinSendPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinSendPlugin) Path() string { return "" }

// Schema exposes a structured description the agent prompt builder renders into
// per-subcommand flag lists with examples.
func (*BuiltinSendPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "send",
				"description": "Deliver a message to a messaging platform. Bare platform uses its configured home channel; platform:chat_id targets a specific chat.",
				"flags": []map[string]interface{}{
					{"name": "to", "type": "string", "required": true, "description": "Target: 'platform' or 'platform:chat_id'. Platforms: telegram|whatsapp|discord|slack|webhook."},
					{"name": "message", "type": "string", "required": true, "description": "The message text to send."},
				},
				"examples": []string{
					`{"cmd":"send","args":{"to":"telegram","message":"Done ✅"}}`,
					`{"cmd":"send","args":{"to":"whatsapp:+5511999999999","message":"Olá!"}}`,
				},
			},
			{
				"name":        "list",
				"description": "List the configured messaging platforms and their default (home) targets.",
				"examples":    []string{`{"cmd":"list"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses the args and dispatches to the adapter.
func (p *BuiltinSendPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — this plugin produces no incremental
// output, so the stream callback is ignored.
func (p *BuiltinSendPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentSendAdapter()
	if adapter == nil {
		return "", errors.New("@send: messaging is not available in this session")
	}
	if len(args) == 0 {
		return "", errors.New(`@send: empty args. Example: <tool_call name="@send" args='{"cmd":"send","args":{"to":"telegram","message":"hi"}}' />`)
	}

	cmd, inner, err := parseSendInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@send: %w", err)
	}

	switch cmd {
	case "send":
		var in struct {
			To      string `json:"to"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.To) == "" {
			return "", errors.New(`@send send: "to" is required (e.g. "telegram" or "telegram:chat_id")`)
		}
		if strings.TrimSpace(in.Message) == "" {
			return "", errors.New(`@send send: "message" is required`)
		}
		return adapter.Send(ctx, in.To, in.Message)
	case "list":
		return adapter.List(ctx)
	default:
		return "", fmt.Errorf("@send: unknown cmd %q (valid: send|list)", cmd)
	}
}

// parseSendInvocation accepts the JSON envelope {"cmd":..,"args":{..}}, flat
// JSON {"cmd":"send","to":..,"message":..}, and the flattened argv form.
// Returns the canonical (cmd, innerJSON).
func parseSendInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(`parse envelope: %w. Expected {"cmd":"send","args":{"to":"...","message":"..."}}`, err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalSendCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: send|list)", cmdStr)
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

	canon := canonicalSendCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf(
			`expected JSON envelope or subcommand; got %q. Example: {"cmd":"send","args":{"to":"telegram","message":"hi"}}`,
			args[0],
		)
	}
	inner, err := sendFlagsToJSON(args[1:])
	if err != nil {
		return "", "", err
	}
	return canon, inner, nil
}

// canonicalSendCmd folds aliases into the two canonical names.
func canonicalSendCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "send", "message", "msg", "notify", "deliver":
		return "send"
	case "list", "targets", "platforms", "channels":
		return "list"
	}
	return ""
}

// sendFlagsToJSON converts ["--to","telegram","--message","hi"] into JSON.
func sendFlagsToJSON(argv []string) (string, error) {
	obj := map[string]interface{}{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			return "", fmt.Errorf("unexpected positional argument %q (use --key value or a JSON envelope)", a)
		}
		key := strings.TrimLeft(a, "-")
		if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
			obj[key] = true
			continue
		}
		obj[key] = argv[i+1]
		i++
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
