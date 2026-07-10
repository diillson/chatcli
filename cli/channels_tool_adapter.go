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
			content = content[:channelsMsgContentMax] + "…"
		}
		fmt.Fprintf(&b, "[seq %d] %s/%s at %s: %s\n",
			msg.Seq, msg.ServerName, msg.Channel,
			msg.Timestamp.Format("15:04:05"), content)
	}
	b.WriteString("Use /channel run <seq> guidance only if the USER asks; " +
		"acknowledge with @channels ack once the inbox has been processed.")
	return b.String()
}
