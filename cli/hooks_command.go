package cli

import (
	"fmt"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/hooks"
)

func (cli *ChatCLI) handleHooksCommand(userInput string) {
	if cli.hookManager == nil {
		fmt.Println(colorize("  Hooks não inicializado.", ColorYellow))
		return
	}

	hks := cli.hookManager.GetHooks()

	fmt.Println()
	fmt.Println(uiBox("⚡", "HOOKS", ColorPurple))
	p := uiPrefix(ColorPurple)

	if len(hks) == 0 {
		fmt.Println(p + colorize("Nenhum hook configurado.", ColorGray))
		fmt.Println(p)
		fmt.Println(p + colorize("Configure hooks em ~/.chatcli/hooks.json:", ColorGray))
		fmt.Println(p)
		fmt.Println(p + colorize(`  { "hooks": [{`, ColorGray))
		fmt.Println(p + colorize(`    "name": "notify-tool",`, ColorGray))
		fmt.Println(p + colorize(`    "event": "PostToolUse",`, ColorGray))
		fmt.Println(p + colorize(`    "type": "command",`, ColorGray))
		fmt.Println(p + colorize(`    "command": "echo \"Tool $CHATCLI_HOOK_TOOL done\""`, ColorGray))
		fmt.Println(p + colorize(`  }]}`, ColorGray))
		fmt.Println(p)
		fmt.Println(p + colorize("Eventos:", ColorCyan))
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
		fmt.Println(p + fmt.Sprintf("    %sEvento:%s  %s  %sTipo:%s  %s",
			ColorGray, ColorReset, string(h.Event),
			ColorGray, ColorReset, string(h.Type)))
		fmt.Println(p + fmt.Sprintf("    %sTarget:%s  %s",
			ColorGray, ColorReset, colorize(target, ColorGray)))
		if h.ToolPattern != "" {
			fmt.Println(p + fmt.Sprintf("    %sFiltro:%s  %s",
				ColorGray, ColorReset, colorize(h.ToolPattern, ColorCyan)))
		}
		fmt.Println(p)
	}

	enabled := 0
	for _, h := range hks {
		if h.IsEnabled() {
			enabled++
		}
	}
	fmt.Println(p + fmt.Sprintf("  %sTotal:%s %d hooks (%s%d ativos%s)",
		ColorGray, ColorReset, len(hks),
		ColorGreen, enabled, ColorReset))
	fmt.Println(uiBoxEnd(ColorPurple))
	fmt.Println()
}

func (cli *ChatCLI) getHooksSuggestions(d prompt.Document) []prompt.Suggest {
	suggestions := []prompt.Suggest{
		{Text: "list", Description: "Lista todos os hooks configurados"},
		{Text: "reload", Description: "Recarrega hooks dos arquivos de configuração"},
	}
	return prompt.FilterHasPrefix(suggestions, d.GetWordBeforeCursor(), true)
}

// Avoid unused import
var _ = strings.TrimSpace
