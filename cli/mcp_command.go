package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
)

func (cli *ChatCLI) handleMCPCommand(userInput string) {
	if cli.mcpManager == nil {
		fmt.Println()
		fmt.Println(uiBox("🔌", i18n.T("mcp.cmd.box_title"), ColorPurple))
		p := uiPrefix(ColorPurple)
		fmt.Println(p + colorize(i18n.T("mcp.cmd.not_enabled"), ColorYellow))
		fmt.Println(p)
		fmt.Println(p + colorize(i18n.T("mcp.cmd.enable_hint"), ColorGray))
		fmt.Println(p)
		fmt.Println(p + colorize(`  {`, ColorGray))
		fmt.Println(p + colorize(`    "mcpServers": [{`, ColorGray))
		fmt.Println(p + colorize(`      "name": "filesystem",`, ColorGray))
		fmt.Println(p + colorize(`      "transport": "stdio",`, ColorGray))
		fmt.Println(p + colorize(`      "command": "npx",`, ColorGray))
		fmt.Println(p + colorize(`      "args": ["-y", "@anthropic/mcp-server-filesystem", "/workspace"],`, ColorGray))
		fmt.Println(p + colorize(`      "enabled": true`, ColorGray))
		fmt.Println(p + colorize(`    }]`, ColorGray))
		fmt.Println(p + colorize(`  }`, ColorGray))
		fmt.Println(p)
		fmt.Println(uiBoxEnd(ColorPurple))
		fmt.Println()
		return
	}

	args := strings.Fields(userInput)
	subcommand := ""
	if len(args) > 1 {
		subcommand = args[1]
	}

	switch subcommand {
	case "tools":
		cli.mcpShowTools()
	case "restart":
		cli.mcpRestart()
	case "status", "":
		cli.mcpShowStatus()
	default:
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.usage"), ColorYellow))
	}
}

func (cli *ChatCLI) mcpShowStatus() {
	statuses := cli.mcpManager.GetServerStatus()
	tools := cli.mcpManager.GetTools()

	fmt.Println()
	fmt.Println(uiBox("🔌", i18n.T("mcp.cmd.box_title"), ColorCyan))
	p := uiPrefix(ColorCyan)

	if len(statuses) == 0 {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.no_servers"), ColorGray))
		fmt.Println(uiBoxEnd(ColorCyan))
		return
	}

	for _, s := range statuses {
		icon := "●"
		statusColor := ColorGreen
		statusText := i18n.T("mcp.cmd.status_connected")
		if !s.Connected {
			icon = "○"
			statusColor = ColorRed
			statusText = i18n.T("mcp.cmd.status_disconnected")
		}

		uptime := ""
		if s.Connected && !s.StartedAt.IsZero() {
			uptime = colorize(fmt.Sprintf(" (%s)", time.Since(s.StartedAt).Truncate(time.Second)), ColorGray)
		}

		fmt.Println(p + fmt.Sprintf("  %s%s%s %s%-15s%s %s%s%s  %s%s%s%s",
			statusColor, icon, ColorReset,
			ColorBold, s.Name, ColorReset,
			statusColor, statusText, ColorReset,
			ColorCyan, i18n.T("mcp.cmd.tools_count", s.ToolCount), ColorReset,
			uptime))

		if s.LastError != nil {
			fmt.Println(p + colorize(fmt.Sprintf("    ↳ %v", s.LastError), ColorRed))
		}
	}

	fmt.Println(p)
	fmt.Println(p + fmt.Sprintf("  %s%s%s %s",
		ColorGray, i18n.T("mcp.cmd.total_label")+":", ColorReset,
		i18n.T("mcp.cmd.total_summary", len(statuses), fmt.Sprintf("%s%d%s", ColorLime, len(tools), ColorReset))))
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}

func (cli *ChatCLI) mcpShowTools() {
	tools := cli.mcpManager.GetTools()

	fmt.Println()
	fmt.Println(uiBox("🔧", i18n.T("mcp.cmd.box_tools_title"), ColorCyan))
	p := uiPrefix(ColorCyan)

	if len(tools) == 0 {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.no_tools"), ColorGray))
		fmt.Println(uiBoxEnd(ColorCyan))
		return
	}

	for _, t := range tools {
		fmt.Println(p + fmt.Sprintf("  %s%s%s", ColorLime, t.Function.Name, ColorReset))
		fmt.Println(p + fmt.Sprintf("  %s%s%s", ColorGray, t.Function.Description, ColorReset))
		if t.Function.Parameters != nil {
			if props, ok := t.Function.Parameters["properties"].(map[string]interface{}); ok {
				for pname := range props {
					fmt.Println(p + fmt.Sprintf("    %s·%s %s", ColorPurple, ColorReset, pname))
				}
			}
		}
		fmt.Println(p)
	}
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}

func (cli *ChatCLI) mcpRestart() {
	fmt.Println()
	fmt.Println(uiBox("🔄", i18n.T("mcp.cmd.box_restart_title"), ColorYellow))
	p := uiPrefix(ColorYellow)
	fmt.Println(p + colorize(i18n.T("mcp.cmd.restarting"), ColorGray))

	cli.mcpManager.StopAll()
	if cli.mcpCancel != nil {
		cli.mcpCancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := cli.mcpManager.StartAll(ctx); err != nil {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.restart_error", err), ColorRed))
		fmt.Println(uiBoxEnd(ColorYellow))
		cancel()
		return
	}

	cli.mcpCancel = cancel
	statuses := cli.mcpManager.GetServerStatus()
	tools := cli.mcpManager.GetTools()
	fmt.Println(p + fmt.Sprintf("%s✓%s %s",
		ColorGreen, ColorReset,
		i18n.T("mcp.cmd.restart_success", len(statuses), len(tools))))
	fmt.Println(uiBoxEnd(ColorYellow))
	fmt.Println()
}

func (cli *ChatCLI) getMCPSuggestions(d prompt.Document) []prompt.Suggest {
	suggestions := []prompt.Suggest{
		{Text: "status", Description: i18n.T("mcp.cmd.sug_status")},
		{Text: "tools", Description: i18n.T("mcp.cmd.sug_tools")},
		{Text: "restart", Description: i18n.T("mcp.cmd.sug_restart")},
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}
