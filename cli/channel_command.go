package cli

import (
	"fmt"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

func (cli *ChatCLI) handleChannelCommand(userInput string) {
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

	default:
		// List (optionally filtered by channel name)
		channelFilter := ""
		if subcommand != "" && subcommand != "list" {
			channelFilter = subcommand
		}

		var msgs []struct {
			ServerName string
			Channel    string
			Content    string
			Timestamp  string
		}
		if channelFilter != "" {
			for _, m := range ch.GetByChannel(channelFilter, 20) {
				msgs = append(msgs, struct {
					ServerName string
					Channel    string
					Content    string
					Timestamp  string
				}{m.ServerName, m.Channel, m.Content, m.Timestamp.Format("15:04:05")})
			}
		} else {
			for _, m := range ch.GetRecent(20) {
				msgs = append(msgs, struct {
					ServerName string
					Channel    string
					Content    string
					Timestamp  string
				}{m.ServerName, m.Channel, m.Content, m.Timestamp.Format("15:04:05")})
			}
		}

		title := i18n.T("chan.cmd.box_title")
		if channelFilter != "" {
			title = fmt.Sprintf(i18n.T("chan.cmd.box_title_filtered"), channelFilter)
		}

		fmt.Println()
		fmt.Println(uiBox("📡", title, ColorCyan))
		p := uiPrefix(ColorCyan)

		if len(msgs) == 0 {
			fmt.Println(p + colorize(i18n.T("chan.cmd.no_messages"), ColorGray))
			fmt.Println(p + colorize(i18n.T("chan.cmd.no_messages_hint"), ColorGray))
		} else {
			for _, m := range msgs {
				fmt.Println(p + fmt.Sprintf("  %s%s%s %s%s%s/%s%s%s %s",
					ColorGray, m.Timestamp, ColorReset,
					ColorCyan, m.ServerName, ColorReset,
					ColorPurple, m.Channel, ColorReset,
					truncateStr(m.Content, 60)))
			}
			fmt.Println(p)
			fmt.Println(p + fmt.Sprintf("  %s%s:%s "+i18n.T("chan.cmd.total_messages"), ColorGray, i18n.T("chan.cmd.total_label"), ColorReset, ch.Count()))
		}

		fmt.Println(uiBoxEnd(ColorCyan))
		fmt.Println()
	}
}

func (cli *ChatCLI) getChannelSuggestions(d prompt.Document) []prompt.Suggest {
	suggestions := []prompt.Suggest{
		{Text: "list", Description: i18n.T("chan.cmd.suggest_list")},
		{Text: "inject", Description: i18n.T("chan.cmd.suggest_inject")},
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}
