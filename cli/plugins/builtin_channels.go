/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinChannelsPlugin — the agent-facing window into the MCP channel
 * inbox. Servers push events (CI alerts, monitoring, protocol notices)
 * into a bounded ring the user browses with /channel; this tool lets the
 * MODEL read the same inbox mid-task and acknowledge what it processed,
 * so pushed context can drive the plan without waiting for the user to
 * paste it.
 *
 * Deliberately read-mostly: list and unread are pure reads; ack only
 * resets attention counters. Pending CONFIRM actions are never exposed —
 * they gate real side effects and stay a human decision (/channel
 * confirm <id>).
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/diillson/chatcli/i18n"
)

// ChannelsAdapter is the interface the @channels builtin uses to reach the
// live MCP channel inbox, bound to the current session.
type ChannelsAdapter interface {
	// ListMessages renders the most recent messages (optionally filtered
	// by channel name) as model-readable text. Empty string when the
	// inbox has no matching messages.
	ListMessages(channel string, limit int) (string, error)
	// UnreadSummary renders the messages received since the last ack.
	// Empty string when everything is acknowledged.
	UnreadSummary() (string, error)
	// Ack acknowledges the inbox: clears the unread counter and the
	// pending notify banner. Returns how many of each were cleared.
	Ack() (notify int, unread int, err error)
}

type channelsAdapterHolder struct{ a ChannelsAdapter }

var channelsAdapterAtom atomic.Value // stores channelsAdapterHolder

// SetChannelsAdapter wires the live adapter. Called from the top-level cli
// package once the MCP manager exists. Pass nil to clear it.
func SetChannelsAdapter(a ChannelsAdapter) {
	channelsAdapterAtom.Store(channelsAdapterHolder{a: a})
}

func currentChannelsAdapter() ChannelsAdapter {
	v := channelsAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(channelsAdapterHolder)
	return h.a
}

// BuiltinChannelsPlugin is the @channels tool.
type BuiltinChannelsPlugin struct{}

// NewBuiltinChannelsPlugin returns a ready-to-register plugin.
func NewBuiltinChannelsPlugin() *BuiltinChannelsPlugin { return &BuiltinChannelsPlugin{} }

// Name returns "@channels".
func (*BuiltinChannelsPlugin) Name() string { return "@channels" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinChannelsPlugin) Description() string {
	return "Read the MCP channel inbox — events pushed by connected MCP servers (CI alerts, monitoring, webhooks, protocol notices). Use 'list' to see recent messages (optionally filtered by channel), 'unread' for what arrived since the last acknowledgment, and 'ack' after processing the inbox so the user's banner clears. Pending confirm actions are user-only and never appear here."
}

// Usage explains the canonical invocation forms.
func (*BuiltinChannelsPlugin) Usage() string {
	return `<tool_call name="@channels" args='{"cmd":"list","args":{"channel":"","limit":10}}' />
<tool_call name="@channels" args='{"cmd":"unread"}' />
<tool_call name="@channels" args='{"cmd":"ack"}' />`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinChannelsPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinChannelsPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders.
func (*BuiltinChannelsPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args}",
		"subcommands": []map[string]interface{}{
			{
				"name":        "list",
				"description": "Most recent inbox messages, newest last. args: {channel (optional filter), limit (default 10, max 50)}.",
			},
			{
				"name":        "unread",
				"description": "Messages received since the last acknowledgment, plus the count.",
			},
			{
				"name":        "ack",
				"description": "Acknowledge the inbox after processing it: clears the unread counter and the pending notify banner.",
			},
		},
		"examples": []string{
			`{"cmd":"list","args":{"limit":5}}`,
			`{"cmd":"list","args":{"channel":"incidents"}}`,
			`{"cmd":"unread"}`,
			`{"cmd":"ack"}`,
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses the args and dispatches to the adapter.
func (p *BuiltinChannelsPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// channelsInvocation is the parsed form of the tool arguments.
type channelsInvocation struct {
	Cmd     string
	Channel string
	Limit   int
}

// parseChannelsInvocation accepts the JSON envelope {cmd,args} as well as
// flattened {cmd,channel,limit} and bare-word forms ("list", "unread").
func parseChannelsInvocation(payload string) channelsInvocation {
	inv := channelsInvocation{Cmd: "list", Limit: 10}
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return inv
	}
	if !strings.HasPrefix(payload, "{") {
		if f := strings.Fields(payload); len(f) > 0 {
			inv.Cmd = strings.ToLower(f[0])
			if len(f) > 1 {
				inv.Channel = f[1]
			}
		}
		return inv
	}
	var raw struct {
		Cmd     string          `json:"cmd"`
		Args    json.RawMessage `json:"args"`
		Channel string          `json:"channel"`
		Limit   int             `json:"limit"`
	}
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return inv
	}
	if raw.Cmd != "" {
		inv.Cmd = strings.ToLower(strings.TrimSpace(raw.Cmd))
	}
	inv.Channel = raw.Channel
	if raw.Limit > 0 {
		inv.Limit = raw.Limit
	}
	if len(raw.Args) > 0 {
		var inner struct {
			Channel string `json:"channel"`
			Limit   int    `json:"limit"`
		}
		if json.Unmarshal(raw.Args, &inner) == nil {
			if inner.Channel != "" {
				inv.Channel = inner.Channel
			}
			if inner.Limit > 0 {
				inv.Limit = inner.Limit
			}
		}
	}
	return inv
}

// canonicalChannelsCmd folds the aliases models tend to guess.
func canonicalChannelsCmd(cmd string) string {
	switch cmd {
	case "list", "ls", "recent", "read", "messages":
		return "list"
	case "unread", "new", "pending", "count":
		return "unread"
	case "ack", "acknowledge", "mark_read", "markread", "seen":
		return "ack"
	default:
		return cmd
	}
}

// ExecuteWithStream mirrors Execute — no incremental output.
func (p *BuiltinChannelsPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentChannelsAdapter()
	if adapter == nil {
		return "", errors.New("@channels: MCP is not enabled in this session")
	}

	inv := parseChannelsInvocation(strings.Join(args, " "))
	if inv.Limit > 50 {
		inv.Limit = 50
	}

	switch canonicalChannelsCmd(inv.Cmd) {
	case "list":
		out, err := adapter.ListMessages(inv.Channel, inv.Limit)
		if err != nil {
			return "", err
		}
		if out == "" {
			return "The MCP channel inbox has no matching messages.", nil
		}
		return out, nil
	case "unread":
		out, err := adapter.UnreadSummary()
		if err != nil {
			return "", err
		}
		if out == "" {
			return "No unread channel messages — the inbox is fully acknowledged.", nil
		}
		return out, nil
	case "ack":
		notify, unread, err := adapter.Ack()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Inbox acknowledged: %d unread message(s) and %d pending notification(s) cleared.", unread, notify), nil
	default:
		return "", fmt.Errorf("%s", i18n.T("plugins.channels.unknown_cmd", inv.Cmd))
	}
}
