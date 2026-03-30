package cli

import (
	"fmt"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/models"
)

func (cli *ChatCLI) handleChannelCommand(userInput string) {
	if cli.mcpManager == nil {
		fmt.Println()
		fmt.Println(uiBox("📡", "MCP CHANNELS", ColorPurple))
		p := uiPrefix(ColorPurple)
		fmt.Println(p + colorize("MCP não habilitado. Channels requerem MCP servers.", ColorYellow))
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
			fmt.Println(colorize("  Nenhuma mensagem para injetar.", ColorGray))
			return
		}
		cli.history = append(cli.history, models.Message{
			Role:    "system",
			Content: promptText,
		})
		fmt.Println(colorize("  ✓ Mensagens injetadas no contexto.", ColorGreen))

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

		title := "MCP CHANNELS"
		if channelFilter != "" {
			title = fmt.Sprintf("CHANNEL: %s", channelFilter)
		}

		fmt.Println()
		fmt.Println(uiBox("📡", title, ColorCyan))
		p := uiPrefix(ColorCyan)

		if len(msgs) == 0 {
			fmt.Println(p + colorize("Nenhuma mensagem recebida.", ColorGray))
			fmt.Println(p + colorize("Channels são preenchidos quando MCP servers enviam push messages.", ColorGray))
		} else {
			for _, m := range msgs {
				fmt.Println(p + fmt.Sprintf("  %s%s%s %s%s%s/%s%s%s %s",
					ColorGray, m.Timestamp, ColorReset,
					ColorCyan, m.ServerName, ColorReset,
					ColorPurple, m.Channel, ColorReset,
					truncateStr(m.Content, 60)))
			}
			fmt.Println(p)
			fmt.Println(p + fmt.Sprintf("  %sTotal:%s %d mensagens", ColorGray, ColorReset, ch.Count()))
		}

		fmt.Println(uiBoxEnd(ColorCyan))
		fmt.Println()
	}
}

func (cli *ChatCLI) getChannelSuggestions(d prompt.Document) []prompt.Suggest {
	suggestions := []prompt.Suggest{
		{Text: "list", Description: "Lista mensagens recentes de channels"},
		{Text: "inject", Description: "Injeta mensagens no contexto da conversa"},
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}
