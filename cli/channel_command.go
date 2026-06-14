/*
 * ChatCLI - /channel command handler
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Subcommands:
 *
 *   list                 default — show recent messages + unread count
 *   <channel-name>       filter list to that channel
 *   inject               splice last 10 messages into the next turn
 *                        as a system message (legacy behavior preserved)
 *   ack                  clear unread + pending notify banner
 *   pause / resume       toggle the trigger engine
 *   rules                show active rule set; `rules reload` re-reads
 *                        ~/.chatcli/mcp/triggers.json
 *   confirm <id> [no]    accept (or deny) a pending confirm action
 *   run <seq>            manually fire the agent on a specific message
 */
package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

func (cli *ChatCLI) handleChannelCommand(ctx context.Context, userInput string) {
	if cli.mcpManager == nil {
		fmt.Println()
		fmt.Println(uiBox("📡", i18n.T("chan.cmd.box_title"), ColorPurple))
		p := uiPrefix(ColorPurple)
		fmt.Println(p + colorize(i18n.T("chan.cmd.mcp_disabled"), ColorYellow))
		fmt.Println(uiBoxEnd(ColorPurple))
		fmt.Println()
		return
	}

	ch := cli.mcpManager.Channels()
	args := strings.Fields(userInput)
	subcommand := ""
	if len(args) > 1 {
		subcommand = args[1]
	}

	switch subcommand {
	case "inject":
		cli.runChannelInject(ch)
	case "ack":
		cli.runChannelAck()
	case "pause":
		cli.channelTriggerPause()
		fmt.Println(colorize("  "+i18n.T("chan.cmd.pause_done"), ColorYellow))
	case "resume":
		cli.channelTriggerResume()
		fmt.Println(colorize("  "+i18n.T("chan.cmd.resume_done"), ColorGreen))
	case "rules":
		cli.runChannelRules(args)
	case "confirm":
		cli.runChannelConfirm(ctx, args)
	case "run":
		cli.runChannelRun(ctx, args)
	default:
		cli.runChannelList(ch, subcommand)
	}
}

// runChannelInject preserves the legacy /channel inject behavior.
func (cli *ChatCLI) runChannelInject(ch interface {
	FormatForPrompt(int) string
}) {
	promptText := ch.FormatForPrompt(10)
	if promptText == "" {
		fmt.Println(colorize("  "+i18n.T("chan.cmd.inject_empty"), ColorGray))
		return
	}
	cli.history = append(cli.history, models.Message{
		Role:    "system",
		Content: promptText,
	})
	fmt.Println(colorize("  ✓ "+i18n.T("chan.cmd.inject_success"), ColorGreen))
}

// runChannelAck clears unread + pending notify banner state.
func (cli *ChatCLI) runChannelAck() {
	notify, unread := cli.channelTriggerAck()
	fmt.Println(colorize(
		"  ✓ "+i18n.T("chan.cmd.ack_done", notify, unread),
		ColorGreen))
}

// runChannelList is the default subcommand — prints recent messages
// optionally filtered by name. Identical to the legacy default minus
// the additional "unread" indicator on the header.
func (cli *ChatCLI) runChannelList(ch channelLister, channelFilter string) {
	if channelFilter == "list" {
		channelFilter = ""
	}

	type msgRow struct {
		Server, Channel, Content, Timestamp string
		Seq                                 uint64
	}
	var msgs []msgRow

	if channelFilter != "" {
		for _, m := range ch.GetByChannel(channelFilter, 20) {
			msgs = append(msgs, msgRow{
				Server: m.ServerName, Channel: m.Channel, Content: m.Content,
				Timestamp: m.Timestamp.Format("15:04:05"), Seq: m.Seq,
			})
		}
	} else {
		for _, m := range ch.GetRecent(20) {
			msgs = append(msgs, msgRow{
				Server: m.ServerName, Channel: m.Channel, Content: m.Content,
				Timestamp: m.Timestamp.Format("15:04:05"), Seq: m.Seq,
			})
		}
	}

	title := i18n.T("chan.cmd.box_title")
	if channelFilter != "" {
		title = i18n.T("chan.cmd.box_title_filtered", channelFilter)
	}

	fmt.Println()
	fmt.Println(uiBox("📡", title, ColorCyan))
	p := uiPrefix(ColorCyan)

	if len(msgs) == 0 {
		fmt.Println(p + colorize(i18n.T("chan.cmd.no_messages"), ColorGray))
		fmt.Println(p + colorize(i18n.T("chan.cmd.no_messages_hint"), ColorGray))
	} else {
		for _, m := range msgs {
			fmt.Println(p + fmt.Sprintf("  #%d %s%s%s %s%s%s/%s%s%s %s",
				m.Seq,
				ColorGray, m.Timestamp, ColorReset,
				ColorCyan, m.Server, ColorReset,
				ColorPurple, m.Channel, ColorReset,
				truncateStr(m.Content, 60)))
		}
		fmt.Println(p)
		fmt.Println(p + fmt.Sprintf("  %s%s:%s ",
			ColorGray, i18n.T("chan.cmd.total_label"), ColorReset) +
			i18n.T("chan.cmd.total_messages", ch.Count()))
		if u := ch.Unread(); u > 0 {
			fmt.Println(p + colorize(
				fmt.Sprintf("  %s: %d", i18n.T("chan.cmd.unread_label"), u),
				ColorYellow))
		}
		if cli.channelTriggerIsPaused() {
			fmt.Println(p + colorize("  "+i18n.T("chan.cmd.paused_note"), ColorGray))
		}
	}

	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}

// channelLister names the slice of ChannelManager that runChannelList
// needs. Stated as an interface so the dependency is minimal and the
// helper stays cheap to test.
type channelLister interface {
	Count() int
	Unread() int
	GetRecent(int) []mcp.ChannelMessage
	GetByChannel(string, int) []mcp.ChannelMessage
}

// runChannelRules shows the active rule set and supports a
// `rules reload` subcommand to re-read the config file.
func (cli *ChatCLI) runChannelRules(args []string) {
	if len(args) >= 3 && args[2] == "reload" {
		n, err := cli.reloadChannelTriggerRules()
		if err != nil {
			fmt.Println(colorize("  ✗ "+err.Error(), ColorYellow))
			return
		}
		fmt.Println(colorize("  ✓ "+i18n.T("chan.cmd.rules_reloaded", n), ColorGreen))
		return
	}
	rules := cli.channelTriggerRules()
	fmt.Println()
	fmt.Println(uiBox("⚙", i18n.T("chan.cmd.rules_title"), ColorBlue))
	p := uiPrefix(ColorBlue)
	if len(rules) == 0 {
		fmt.Println(p + colorize(i18n.T("chan.cmd.rules_empty"), ColorGray))
	} else {
		for _, r := range rules {
			mode := string(r.Mode)
			if mode == "" {
				mode = "notify"
			}
			fmt.Println(p + fmt.Sprintf("  %s%s%s [%s] server=%s channel=%s",
				ColorYellow, r.Name, ColorReset,
				mode, defaultIfEmpty(r.Server, "*"), defaultIfEmpty(r.Channel, "*")))
			if r.ContentRegex != "" {
				fmt.Println(p + fmt.Sprintf("    %scontentRegex:%s %s",
					ColorGray, ColorReset, r.ContentRegex))
			}
			if r.RateLimitTxt != "" || r.DedupTxt != "" {
				fmt.Println(p + fmt.Sprintf("    %srate:%s %s  %sdedup:%s %s",
					ColorGray, ColorReset, defaultIfEmpty(r.RateLimitTxt, "—"),
					ColorGray, ColorReset, defaultIfEmpty(r.DedupTxt, "—")))
			}
			if len(r.Tools) > 0 {
				fmt.Println(p + fmt.Sprintf("    %stools:%s %s",
					ColorGray, ColorReset, strings.Join(r.Tools, ", ")))
			}
		}
	}
	fmt.Println(uiBoxEnd(ColorBlue))
	fmt.Println()
}

// runChannelConfirm resolves a pending confirm action. Defaults to
// accept when only the id is provided; explicit "no" denies it.
func (cli *ChatCLI) runChannelConfirm(ctx context.Context, args []string) {
	if len(args) < 3 {
		fmt.Println(colorize("  "+i18n.T("chan.cmd.confirm_usage"), ColorYellow))
		return
	}
	id, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("chan.cmd.confirm_bad_id"), ColorYellow))
		return
	}
	accept := true
	if len(args) >= 4 {
		switch strings.ToLower(args[3]) {
		case "no", "n", "deny":
			accept = false
		}
	}
	if err := cli.channelTriggerConfirm(ctx, id, accept); err != nil {
		fmt.Println(colorize("  ✗ "+err.Error(), ColorYellow))
		return
	}
	if !accept {
		fmt.Println(colorize("  "+i18n.T("chan.cmd.confirm_denied", id), ColorGray))
	}
}

// runChannelRun manually triggers the agent on a stored channel
// message. The user picks the seq from the /channel list output.
func (cli *ChatCLI) runChannelRun(ctx context.Context, args []string) {
	if len(args) < 3 {
		fmt.Println(colorize("  "+i18n.T("chan.cmd.run_usage"), ColorYellow))
		return
	}
	seq, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("chan.cmd.run_bad_seq"), ColorYellow))
		return
	}
	if err := cli.channelTriggerRun(ctx, seq); err != nil {
		fmt.Println(colorize("  ✗ "+err.Error(), ColorYellow))
		return
	}
}

// defaultIfEmpty mirrors the JS-style fallback so columns stay
// dense in the rules table.
func defaultIfEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func (cli *ChatCLI) getChannelSuggestions(d prompt.Document) []prompt.Suggest {
	suggestions := []prompt.Suggest{
		{Text: "list", Description: i18n.T("chan.cmd.suggest_list")},
		{Text: "inject", Description: i18n.T("chan.cmd.suggest_inject")},
		{Text: "ack", Description: i18n.T("chan.cmd.suggest_ack")},
		{Text: "pause", Description: i18n.T("chan.cmd.suggest_pause")},
		{Text: "resume", Description: i18n.T("chan.cmd.suggest_resume")},
		{Text: "rules", Description: i18n.T("chan.cmd.suggest_rules")},
		{Text: "confirm", Description: i18n.T("chan.cmd.suggest_confirm")},
		{Text: "run", Description: i18n.T("chan.cmd.suggest_run")},
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}
