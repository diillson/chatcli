package cli

import (
	"fmt"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/i18n"
)

func (cli *ChatCLI) handleHooksCommand(userInput string) {
	if cli.hookManager == nil {
		fmt.Println(colorize("  "+i18n.T("hooks.cmd.not_initialized"), ColorYellow))
		return
	}

	hks := cli.hookManager.GetHooks()

	fmt.Println()
	fmt.Println(uiBox("⚡", i18n.T("hooks.cmd.box_title"), ColorPurple))
	p := uiPrefix(ColorPurple)

	if len(hks) == 0 {
		fmt.Println(p + colorize(i18n.T("hooks.cmd.none_configured"), ColorGray))
		fmt.Println(p)
		fmt.Println(p + colorize(i18n.T("hooks.cmd.configure_in"), ColorGray))
		fmt.Println(p)
		fmt.Println(p + colorize(`  { "hooks": [{`, ColorGray))
		fmt.Println(p + colorize(`    "name": "notify-tool",`, ColorGray))
		fmt.Println(p + colorize(`    "event": "PostToolUse",`, ColorGray))
		fmt.Println(p + colorize(`    "type": "command",`, ColorGray))
		fmt.Println(p + colorize(`    "command": "echo \"Tool $CHATCLI_HOOK_TOOL done\""`, ColorGray))
		fmt.Println(p + colorize(`  }]}`, ColorGray))
		fmt.Println(p)
		fmt.Println(p + colorize(i18n.T("hooks.cmd.events_label"), ColorCyan))
		for _, evt := range hooks.AllEventTypes {
			fmt.Println(p + "  " + colorize(string(evt), ColorGray))
		}
		fmt.Println(p)
		fmt.Println(uiBoxEnd(ColorPurple))
		fmt.Println()
		return
	}

	for _, h := range hks {
		icon := "●"
		statusColor := ColorGreen
		if !h.IsEnabled() {
			icon = "○"
			statusColor = ColorGray
		}

		target := h.Command
		if h.Type == "http" {
			target = h.URL
		}

		fmt.Println(p + fmt.Sprintf("  %s%s%s %s%s%s",
			statusColor, icon, ColorReset,
			ColorBold, h.Name, ColorReset))
		fmt.Println(p + fmt.Sprintf("    %s%s%s  %s  %s%s%s  %s",
			ColorGray, i18n.T("hooks.cmd.event_label"), ColorReset, string(h.Event),
			ColorGray, i18n.T("hooks.cmd.type_label"), ColorReset, string(h.Type)))
		fmt.Println(p + fmt.Sprintf("    %s%s%s  %s",
			ColorGray, i18n.T("hooks.cmd.target_label"), ColorReset, colorize(target, ColorGray)))
		if h.ToolPattern != "" {
			fmt.Println(p + fmt.Sprintf("    %s%s%s  %s",
				ColorGray, i18n.T("hooks.cmd.filter_label"), ColorReset, colorize(h.ToolPattern, ColorCyan)))
		}
		fmt.Println(p)
	}

	enabled := 0
	for _, h := range hks {
		if h.IsEnabled() {
			enabled++
		}
	}
	fmt.Println(p + fmt.Sprintf("  %s%s%s %s",
		ColorGray, i18n.T("hooks.cmd.total_label"), ColorReset,
		i18n.T("hooks.cmd.total_summary", len(hks), ColorGreen, enabled, ColorReset)))
	fmt.Println(uiBoxEnd(ColorPurple))
	fmt.Println()
}

func (cli *ChatCLI) getHooksSuggestions(d prompt.Document) []prompt.Suggest {
	suggestions := []prompt.Suggest{
		{Text: "list", Description: i18n.T("hooks.cmd.suggest_list")},
		{Text: "reload", Description: i18n.T("hooks.cmd.suggest_reload")},
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}

// Avoid unused import
var _ = strings.TrimSpace
