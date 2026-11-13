/*
 * ChatCLI - @channels Tool Adapter
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Bridges the @channels builtin (cli/plugins/builtin_channels.go) to the
 * live MCP channel inbox and the trigger-banner state owned by ChatCLI.
 * Rendering is model-facing plain text: seq + server/channel + timestamp
 * per line, content clamped so a chatty server cannot flood the context.
 */
package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/pkg/textfold"
)

// channelsMsgContentMax clamps each message body inside tool output. The
// full content remains in the ring (and audit file) — the model can ask
// the user or re-list with a channel filter when it truly needs more.
const channelsMsgContentMax = 600

// channelsPluginAdapter implements plugins.ChannelsAdapter.
type channelsPluginAdapter struct {
	cli *ChatCLI
}

func (a *channelsPluginAdapter) manager() (*mcp.Manager, error) {
	if a.cli == nil || a.cli.mcpManager == nil {
		return nil, errors.New("MCP is not enabled in this session")
	}
	return a.cli.mcpManager, nil
}

// ListMessages renders the most recent inbox messages, oldest first so
// the model reads them chronologically.
func (a *channelsPluginAdapter) ListMessages(channel string, limit int) (string, error) {
	m, err := a.manager()
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 10
	}
	var msgs []mcp.ChannelMessage
	if channel != "" {
		msgs = m.Channels().GetByChannel(channel, limit)
	} else {
		msgs = m.Channels().GetRecent(limit)
	}
	return renderChannelMessagesForModel(msgs, m.Channels().Unread()), nil
}

// UnreadSummary renders only what arrived since the last acknowledgment.
func (a *channelsPluginAdapter) UnreadSummary() (string, error) {
	m, err := a.manager()
	if err != nil {
		return "", err
	}
	msgs := m.Channels().UnreadSince()
	if len(msgs) == 0 {
		return "", nil
	}
	return renderChannelMessagesForModel(msgs, len(msgs)), nil
}

// RulesSummary implements plugins.ChannelsRuleLister: the rule set the
// user sees in /channel rules, as plain English text for the model.
func (a *channelsPluginAdapter) RulesSummary() (string, error) {
	if _, err := a.manager(); err != nil {
		return "", err
	}
	rules := a.cli.channelTriggerRules()
	if len(rules) == 0 {
		return "No channel trigger rules are active: incoming messages only land in the inbox.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d channel trigger rule(s)", len(rules))
	if a.cli.channelTriggerIsPaused() {
		b.WriteString(" — the trigger engine is PAUSED by the user, nothing fires")
	}
	b.WriteString(":\n")
	for _, r := range rules {
		fmt.Fprintf(&b, "- %s [%s] server=%s channel=%s", r.Name, defaultIfEmpty(string(r.Mode), "notify"),
			defaultIfEmpty(r.Server, "*"), defaultIfEmpty(r.Channel, "*"))
		for _, kv := range [][2]string{{"contentRegex", r.ContentRegex}, {"rateLimit", r.RateLimitTxt},
			{"dedupWindow", r.DedupTxt}, {"tools", strings.Join(r.Tools, ",")}} {
			if kv[1] != "" {
				fmt.Fprintf(&b, " %s=%s", kv[0], kv[1])
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("Read-only view: rules are edited and reloaded by the user (/channel rules reload).")
	return b.String(), nil
}

// Ack clears unread + pending notify state through the same path as the
// user's /channel ack, so both surfaces stay consistent.
func (a *channelsPluginAdapter) Ack() (int, int, error) {
	if _, err := a.manager(); err != nil {
		return 0, 0, err
	}
	notify, unread := a.cli.channelTriggerAck()
	return notify, unread, nil
}

// renderChannelMessagesForModel formats inbox messages as plain text for
// the model. English on purpose — tool output consumed by the LLM.
func renderChannelMessagesForModel(msgs []mcp.ChannelMessage, unread int) string {
	if len(msgs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "MCP channel inbox — %d message(s) shown, %d unread:\n", len(msgs), unread)
	for _, msg := range msgs {
		content := strings.TrimSpace(strings.ReplaceAll(msg.Content, "\n", " "))
		if len(content) > channelsMsgContentMax {
			content = textfold.Cut(content, channelsMsgContentMax) + "…"
		}
		fmt.Fprintf(&b, "[seq %d] %s/%s at %s: %s\n",
			msg.Seq, msg.ServerName, msg.Channel,
			msg.Timestamp.Format("15:04:05"), content)
	}
	b.WriteString("Use /channel run <seq> guidance only if the USER asks; " +
		"acknowledge with @channels ack once the inbox has been processed.")
	return b.String()
}
