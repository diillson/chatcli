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
		fmt.Println(p + colorize(`      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"],`, ColorGray))
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
	name := ""
	if len(args) > 2 {
		name = args[2]
	}

	switch subcommand {
	case "", "status":
		cli.mcpShowStatus(name)
	case "tools":
		cli.mcpShowTools(name)
	case "restart":
		cli.mcpRestart(name)
	case "start":
		cli.mcpStart(name)
	case "stop":
		cli.mcpStop(name)
	case "reload":
		cli.mcpReload()
	case "logs":
		cli.mcpLogs(name)
	default:
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.usage"), ColorYellow))
	}
}

func (cli *ChatCLI) mcpShowStatus(filter string) {
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

	matched := 0
	for _, s := range statuses {
		if filter != "" && s.Name != filter {
			continue
		}
		matched++
		icon := "●"
		statusColor := ColorGreen
		statusText := i18n.T("mcp.cmd.status_connected")
		switch {
		case s.Connected:
			// keep green/●
		case s.Starting:
			icon = "◌"
			statusColor = ColorYellow
			statusText = i18n.T("mcp.cmd.status_starting")
		default:
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

	if filter != "" && matched == 0 {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.unknown_server", filter), ColorRed))
		fmt.Println(uiBoxEnd(ColorCyan))
		return
	}

	if filter == "" {
		fmt.Println(p)
		fmt.Println(p + fmt.Sprintf("  %s%s%s %s",
			ColorGray, i18n.T("mcp.cmd.total_label")+":", ColorReset,
			i18n.T("mcp.cmd.total_summary", len(statuses), fmt.Sprintf("%s%d%s", ColorLime, len(tools), ColorReset))))
	}
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}

func (cli *ChatCLI) mcpShowTools(filter string) {
	tools := cli.mcpManager.GetTools()

	fmt.Println()
	fmt.Println(uiBox("🔧", i18n.T("mcp.cmd.box_tools_title"), ColorCyan))
	p := uiPrefix(ColorCyan)

	if filter != "" && !cli.mcpServerExists(filter) {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.unknown_server", filter), ColorRed))
		fmt.Println(uiBoxEnd(ColorCyan))
		return
	}

	if len(tools) == 0 {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.no_tools"), ColorGray))
		fmt.Println(uiBoxEnd(ColorCyan))
		return
	}

	wantPrefix := ""
	if filter != "" {
		wantPrefix = fmt.Sprintf("[MCP:%s]", filter)
	}

	shown := 0
	for _, t := range tools {
		if wantPrefix != "" && !strings.HasPrefix(t.Function.Description, wantPrefix) {
			continue
		}
		shown++
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
	if filter != "" && shown == 0 {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.no_tools_for_server", filter), ColorGray))
	}
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}

func (cli *ChatCLI) mcpRestart(name string) {
	if name != "" {
		cli.mcpRestartOne(name)
		return
	}

	fmt.Println()
	fmt.Println(uiBox("🔄", i18n.T("mcp.cmd.box_restart_title"), ColorYellow))
	p := uiPrefix(ColorYellow)
	fmt.Println(p + colorize(i18n.T("mcp.cmd.restarting"), ColorGray))

	cli.mcpManager.StopAll()
	if cli.mcpCancel != nil {
		cli.mcpCancel()
	}

	mcpCtx, cancel := context.WithCancel(context.Background())
	if err := cli.mcpManager.StartAll(mcpCtx); err != nil {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.restart_error", err), ColorRed))
		fmt.Println(uiBoxEnd(ColorYellow))
		cancel()
		return
	}

	cli.mcpCtx = mcpCtx
	cli.mcpCancel = cancel
	statuses := cli.mcpManager.GetServerStatus()
	tools := cli.mcpManager.GetTools()
	fmt.Println(p + fmt.Sprintf("%s✓%s %s",
		ColorGreen, ColorReset,
		i18n.T("mcp.cmd.restart_success", len(statuses), len(tools))))
	fmt.Println(uiBoxEnd(ColorYellow))
	fmt.Println()
}

func (cli *ChatCLI) mcpRestartOne(name string) {
	if !cli.mcpServerExists(name) {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.unknown_server", name), ColorRed))
		return
	}
	fmt.Println()
	fmt.Println(uiBox("🔄", i18n.T("mcp.cmd.box_restart_title"), ColorYellow))
	p := uiPrefix(ColorYellow)
	fmt.Println(p + colorize(i18n.T("mcp.cmd.restarting_one", name), ColorGray))

	if err := cli.mcpManager.StopOne(name); err != nil {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.restart_error", err), ColorRed))
		fmt.Println(uiBoxEnd(ColorYellow))
		return
	}
	if err := cli.mcpManager.StartOne(cli.mcpCtx, name); err != nil {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.restart_error", err), ColorRed))
		fmt.Println(uiBoxEnd(ColorYellow))
		return
	}
	fmt.Println(p + fmt.Sprintf("%s✓%s %s", ColorGreen, ColorReset, i18n.T("mcp.cmd.restart_one_success", name)))
	fmt.Println(uiBoxEnd(ColorYellow))
	fmt.Println()
}

func (cli *ChatCLI) mcpStart(name string) {
	if name == "" {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.usage_start"), ColorYellow))
		return
	}
	if err := cli.mcpManager.StartOne(cli.mcpCtx, name); err != nil {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.start_error", err), ColorRed))
		return
	}
	fmt.Println(colorize("  "+i18n.T("mcp.cmd.start_success", name), ColorGreen))
}

func (cli *ChatCLI) mcpStop(name string) {
	if name == "" {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.usage_stop"), ColorYellow))
		return
	}
	if err := cli.mcpManager.StopOne(name); err != nil {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.stop_error", err), ColorRed))
		return
	}
	fmt.Println(colorize("  "+i18n.T("mcp.cmd.stop_success", name), ColorGreen))
}

func (cli *ChatCLI) mcpReload() {
	if cli.mcpConfigPath == "" {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.reload_no_path"), ColorYellow))
		return
	}
	diff, err := cli.mcpManager.Reload(cli.mcpCtx, cli.mcpConfigPath)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.reload_error", err), ColorRed))
		return
	}
	total := len(diff.Started) + len(diff.Stopped) + len(diff.Updated)
	if total == 0 {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.reload_no_changes"), ColorGray))
		return
	}
	fmt.Println(colorize(
		fmt.Sprintf("  ✓ %s",
			i18n.T("mcp.cmd.reload_summary",
				len(diff.Started), len(diff.Stopped), len(diff.Updated))),
		ColorGreen))
	if len(diff.Started) > 0 {
		fmt.Println(colorize("    + "+strings.Join(diff.Started, ", "), ColorLime))
	}
	if len(diff.Stopped) > 0 {
		fmt.Println(colorize("    - "+strings.Join(diff.Stopped, ", "), ColorRed))
	}
	if len(diff.Updated) > 0 {
		fmt.Println(colorize("    ~ "+strings.Join(diff.Updated, ", "), ColorYellow))
	}
}

func (cli *ChatCLI) mcpLogs(name string) {
	if name == "" {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.usage_logs"), ColorYellow))
		return
	}
	if !cli.mcpServerExists(name) {
		fmt.Println(colorize("  "+i18n.T("mcp.cmd.unknown_server", name), ColorRed))
		return
	}
	lines := cli.mcpManager.RecentLogs(name)
	fmt.Println()
	fmt.Println(uiBox("📜", i18n.T("mcp.cmd.box_logs_title", name), ColorCyan))
	p := uiPrefix(ColorCyan)
	if len(lines) == 0 {
		fmt.Println(p + colorize(i18n.T("mcp.cmd.no_logs"), ColorGray))
		fmt.Println(uiBoxEnd(ColorCyan))
		return
	}
	for _, line := range lines {
		fmt.Println(p + "  " + line)
	}
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}

// mcpServerExists returns true when the named server is currently
// configured (either running or stopped-but-known). Used to give a
// crisp "unknown server" message instead of a confusing pass-through.
func (cli *ChatCLI) mcpServerExists(name string) bool {
	for _, s := range cli.mcpManager.GetServerStatus() {
		if s.Name == name {
			return true
		}
	}
	return false
}

// getMCPSuggestions powers /mcp completion. Two completion phases:
//
//  1. Right after `/mcp ` — suggest the subcommand list.
//  2. After a subcommand that takes a server name — suggest the names
//     of currently configured servers. This makes start/stop/restart/
//     logs/status/tools self-discoverable; the user does not need to
//     remember the exact server name they chose in mcp_servers.json.
func (cli *ChatCLI) getMCPSuggestions(d prompt.Document) []prompt.Suggest {
	subcommands := []prompt.Suggest{
		{Text: "status", Description: i18n.T("mcp.cmd.sug_status")},
		{Text: "tools", Description: i18n.T("mcp.cmd.sug_tools")},
		{Text: "restart", Description: i18n.T("mcp.cmd.sug_restart")},
		{Text: "start", Description: i18n.T("mcp.cmd.sug_start")},
		{Text: "stop", Description: i18n.T("mcp.cmd.sug_stop")},
		{Text: "reload", Description: i18n.T("mcp.cmd.sug_reload")},
		{Text: "logs", Description: i18n.T("mcp.cmd.sug_logs")},
	}

	// Tokenize what the user has typed so far (excluding the leading
	// "/mcp"). Position 0 = subcommand, position 1 = server name.
	text := d.TextBeforeCursor()
	fields := strings.Fields(text)
	// Strip the leading "/mcp" token if present so the index lines up
	// with subcommand/argument positions regardless of trailing space.
	if len(fields) > 0 && fields[0] == "/mcp" {
		fields = fields[1:]
	}
	endsWithSpace := strings.HasSuffix(text, " ")

	switch {
	case len(fields) == 0, len(fields) == 1 && !endsWithSpace:
		return prompt.FilterHasPrefix(subcommands, d.GetWordBeforeCursor(), true)
	case (len(fields) == 1 && endsWithSpace) || (len(fields) == 2 && !endsWithSpace):
		// Second position: only some subcommands take a server name.
		sub := fields[0]
		if !mcpSubcommandTakesServerName(sub) {
			return nil
		}
		if cli.mcpManager == nil {
			return nil
		}
		names := cli.mcpManager.ServerNames()
		out := make([]prompt.Suggest, 0, len(names))
		for _, n := range names {
			out = append(out, prompt.Suggest{Text: n})
		}
		return prompt.FilterHasPrefix(out, d.GetWordBeforeCursor(), true)
	}
	return nil
}

func mcpSubcommandTakesServerName(sub string) bool {
	switch sub {
	case "start", "stop", "restart", "logs", "status", "tools":
		return true
	}
	return false
}
