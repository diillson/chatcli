package cli

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/version"
	"github.com/mattn/go-runewidth"
)

// Dicas agora contêm as chaves de tradução.
var tipKeys = []string{ // <-- 2. ALTERADO DE 'tips' PARA 'tipKeys'
	"tip.add_file",
	"tip.git_context",
	"tip.exec_command",
	"tip.switch_provider",
	"tip.new_session",
	"tip.view_config",
	"tip.cancel_request",
	"tip.agent_mode",
	"tip.agent_toggle_view",
	"tip.agent_output_actions",
	"tip.agent_last_result",
}

const screenWidth = 87 // largura global para tudo

// printLogo exibe o novo logo do ChatCLI em ASCII art.
func printLogo() {
	logo := `
           ██████╗ ██╗  ██╗ █████╗ ████████╗ ██████╗██╗     ██╗
          ██╔════╝ ██║  ██║██╔══██╗╚══██╔══╝██╔════╝██║     ██║
          ██║      ███████║███████║   ██║   ██║     ██║     ██║
          ██║      ██╔══██║██╔══██║   ██║   ██║     ██║     ██║
          ╚██████╗ ██║  ██║██║  ██║   ██║   ╚██████╗███████╗██║
           ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝   ╚═╝    ╚═════╝╚══════╝╚═╝
        `

	coloredLogo := strings.ReplaceAll(logo, "█", colorize("█", ColorLime))
	coloredLogo = strings.ReplaceAll(coloredLogo, "╗", colorize("╗", ColorGray))
	coloredLogo = strings.ReplaceAll(coloredLogo, "╔", colorize("╔", ColorGray))
	coloredLogo = strings.ReplaceAll(coloredLogo, "╚", colorize("╚", ColorGray))
	coloredLogo = strings.ReplaceAll(coloredLogo, "╝", colorize("╝", ColorGray))
	coloredLogo = strings.ReplaceAll(coloredLogo, "═", colorize("═", ColorGray))
	coloredLogo = strings.ReplaceAll(coloredLogo, "║", colorize("║", ColorGray))

	width := 80
	for _, line := range strings.Split(coloredLogo, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// calcula padding
		visible := visibleLen(line)
		if visible < width {
			left := (width - visible) / 2
			fmt.Println(strings.Repeat(" ", left) + line)
		} else {
			fmt.Println(line)
		}
	}
}

// --- util: ANSI / largura visível (conta runas, ignora cores) ---
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func removeColorCodes(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

func visibleLen(s string) int {
	return runewidth.StringWidth(removeColorCodes(s))
}

// --- quebra preservando códigos ANSI ---
func wrapStringWithColor(text string, maxWidth int) []string {
	if maxWidth <= 0 {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var lines []string
	var b strings.Builder
	curr := 0

	for _, w := range words {
		wlen := visibleLen(w)

		// se não cabe na linha atual, pula pra próxima
		if curr > 0 && curr+1+wlen > maxWidth {
			lines = append(lines, b.String())
			b.Reset()
			curr = 0
		}
		if curr > 0 {
			b.WriteByte(' ')
			curr++
		}
		b.WriteString(w)
		curr += wlen
	}
	if b.Len() > 0 {
		lines = append(lines, b.String())
	}
	return lines
}

// --- caixa de dica (agora traduzida) ---
func printTipBox() {
	// 3. SORTEIA UMA CHAVE E TRADUZ O TEXTO DA DICA E O TÍTULO
	tipKey := tipKeys[rand.Intn(len(tipKeys))]
	tip := i18n.T(tipKey) // Traduz a dica sorteada

	width := screenWidth
	innerContent := width - 4
	title := i18n.T("welcome.tip.title") // Traduz o título da caixa

	titleWithSpaces := " " + title + " "
	tl := visibleLen(titleWithSpaces)
	dash := width - 2 - tl
	left := dash / 2
	right := dash - left

	fmt.Println(
		colorize("╭", ColorGray) +
			strings.Repeat("─", left) +
			titleWithSpaces +
			strings.Repeat("─", right) +
			colorize("╮", ColorGray),
	)

	// linha em branco
	fmt.Println(colorize("│", ColorGray) + strings.Repeat(" ", width-2) + colorize("│", ColorGray))

	// conteúdo centralizado
	for _, line := range wrapStringWithColor(tip, innerContent) {
		l := visibleLen(line)
		left := (innerContent - l) / 2
		right := innerContent - l - left
		fmt.Println(
			colorize("│", ColorGray) +
				" " + strings.Repeat(" ", left) + line + strings.Repeat(" ", right) + " " +
				colorize("│", ColorGray),
		)
	}

	fmt.Println(colorize("│", ColorGray) + strings.Repeat(" ", width-2) + colorize("│", ColorGray))
	fmt.Println(colorize("╰"+strings.Repeat("─", width-2)+"╯", ColorGray))
}

// PrintWelcomeScreen exibe a tela de boas-vindas completa e traduzida.
func (cli *ChatCLI) PrintWelcomeScreen() {
	printLogo()

	v, c, _ := version.GetBuildInfo()
	if v != "" && v != "dev" && v != "unknown" {
		versionStr := i18n.T("version.label", v, c)
		padding := (screenWidth - visibleLen(versionStr)) / 2
		fmt.Printf("%s%s\n\n", strings.Repeat(" ", padding), colorize(versionStr, ColorGray))
	}

	printTipBox()

	footer := colorize(i18n.T("welcome.footer.help.cmd"), ColorGreen) +
		colorize(" "+i18n.T("welcome.footer.help.desc"), ColorGray) +
		colorize("  •  ", ColorGray) +
		colorize(i18n.T("welcome.footer.exit.cmd"), ColorGreen) +
		colorize(" "+i18n.T("welcome.footer.exit.desc"), ColorGray) +
		colorize("  •  ", ColorGray) +
		colorize(i18n.T("welcome.footer.switch_model.cmd"), ColorGreen) +
		colorize(" "+i18n.T("welcome.footer.switch_model.desc"), ColorGray)

	separator := colorize(strings.Repeat("━", screenWidth), ColorGray)

	fmt.Println()
	// footer alinhado à esquerda
	fmt.Println(footer)
	fmt.Println(separator)

	// model info alinhado à esquerda
	if cli.Client != nil {
		modelInfo := i18n.T("welcome.current_model", cli.Client.GetModelName(), cli.Provider)
		fmt.Println(colorize(modelInfo, ColorLime))
	} else {
		fmt.Println(colorize(i18n.T("welcome.current_model", "(none)", "No provider"), ColorYellow))
		fmt.Println(colorize("  Use /auth login anthropic | openai-codex | github-copilot to authenticate.", ColorGray))
	}
	fmt.Println()
}
