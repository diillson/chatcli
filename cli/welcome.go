package cli

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/diillson/chatcli/version"
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

// welcomeAnchor is the preferred anchor width used to center welcome-screen
// content (logo, tip box, active-model card, footer). 87 columns by design:
// anchoring at the terminal width left the banner pushed far to the right on
// wide terminals — the welcome reads more naturally hugging the left edge
// like the rest of the prompt does.
const welcomeAnchor = 87

// screenWidth resolves the actual anchor for this render: the preferred 87
// columns, shrunk to the live content width on narrow terminals so the tip
// box and cards never overflow and tear their borders.
func screenWidth() int {
	if w := kit.ContentWidth(); w < welcomeAnchor {
		return w
	}
	return welcomeAnchor
}

// printLogo exibe o novo logo do ChatCLI em ASCII art, centralizado
// num bloco virtual de 80 colunas (mesma largura usada antes da
// refatoração de envelope). Esse bloco fica na borda esquerda do
// terminal, por preferência do usuário — uma centralização no
// terminal inteiro empurra o banner pra direita em telas largas.
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

// visibleLen mede largura visível em colunas — delegate do caminho único de
// medição do kit (mantido porque testes do pacote o exercitam diretamente).
func visibleLen(s string) int {
	return kit.VisibleLen(s)
}

// tipBoxBorderStyle is the rounded border used for the welcome tip box and
// the active-model card. Resolved per render (not a package var) so the
// border follows the ACTIVE theme's border role — the old hardcoded
// lipgloss.Color("8") bypassed the theme system entirely.
func tipBoxBorderStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Lip(theme.RoleBorder))
}

// printTipBox renders a centered "Did you know?" tip card. lipgloss
// owns the border drawing and width math so a future change to the
// box style (different border, padding tweak, color) is a one-liner
// instead of a fistful of strings.Repeat calls.
func printTipBox() {
	tipKey := tipKeys[rand.Intn(len(tipKeys))] //#nosec G404 -- non-cryptographic: picks a welcome-screen tip
	tip := i18n.T(tipKey)
	title := i18n.T("welcome.tip.title")
	anchor := screenWidth()

	// Inner content width = card width − 2 borders − 2 of padding.
	innerWidth := anchor - 4

	// Title line centered above the body, in the theme's header role —
	// baked into the body because lipgloss's default border doesn't accept
	// a "title within the top edge".
	titleLine := kit.Style(theme.RoleHeader).
		Width(innerWidth).
		Align(lipgloss.Center).
		Bold(true).
		Render(title)

	// Wrap the tip body with the kit's prose word-wrap (ANSI-aware) and
	// leave the text in the terminal's default color — the tip is content,
	// not chrome.
	wrapped := strings.Join(kit.WrapText(tip, innerWidth), "\n")
	body := lipgloss.NewStyle().
		Width(innerWidth).
		Align(lipgloss.Center).
		Render(wrapped)

	card := tipBoxBorderStyle().
		Padding(1, 1).
		Render(titleLine + "\n\n" + body)

	// Center the card on the resolved anchor so the overall welcome layout
	// stays balanced.
	fmt.Println(lipgloss.PlaceHorizontal(anchor, lipgloss.Center, card))
}

// PrintWelcomeScreen exibe a tela de boas-vindas completa e traduzida.
//
// Layout (todos centrados em screenWidth):
//
//	<ASCII logo>
//	v1.2.3 · commit abc123
//	╭── Did you know? ──╮
//	│   <tip>           │
//	╰───────────────────╯
//	╭── Active model ────╮
//	│ ◆ name · provider │
//	╰────────────────────╯
//	/help · /exit · /switch
//
// The shift to centered + boxed Active-model block came with PR3:
// before it was left-aligned plain text while everything else was
// centered, which read as "two screens spliced together".
func (cli *ChatCLI) PrintWelcomeScreen() {
	printLogo()
	anchor := screenWidth()

	v, c, _ := version.GetBuildInfo()
	if v != "" && v != "dev" && v != "unknown" {
		versionStr := i18n.T("version.label", v, c)
		fmt.Println(lipgloss.PlaceHorizontal(anchor, lipgloss.Center,
			colorize(versionStr, ColorGray)))
		fmt.Println()
	}

	printTipBox()

	// Active-model card. Same border style as the tip box so the two
	// sit visually balanced on the screen. Falls back to a "no model"
	// state with a hint when no provider is wired up.
	var modelLine string
	if cli.Client != nil {
		modelLine = lipgloss.JoinHorizontal(lipgloss.Top,
			colorize("◆ ", ColorLime),
			colorize(cli.Client.GetModelName(), ColorLime+ColorBold),
			colorize(" · ", ColorGray),
			colorize(cli.Provider, ColorGray),
		)
	} else {
		modelLine = lipgloss.JoinVertical(lipgloss.Left,
			colorize("◆ "+i18n.T("welcome.current_model", "(none)", "No provider"), ColorYellow),
			colorize(i18n.T("welcome.auth_hint"), ColorGray),
		)
	}
	modelCard := tipBoxBorderStyle().
		Padding(0, 2).
		Render(modelLine)
	fmt.Println(lipgloss.PlaceHorizontal(anchor, lipgloss.Center, modelCard))

	// Footer of quick commands, centered to match the rest of the
	// layout. Plain Bullet (·) instead of the heavier "  •  " for a
	// lighter look that pairs better with the lipgloss-rendered cards.
	footer := lipgloss.JoinHorizontal(lipgloss.Top,
		colorize(i18n.T("welcome.footer.help.cmd"), ColorGreen),
		colorize(" "+i18n.T("welcome.footer.help.desc"), ColorGray),
		colorize("  ·  ", ColorGray),
		colorize(i18n.T("welcome.footer.exit.cmd"), ColorGreen),
		colorize(" "+i18n.T("welcome.footer.exit.desc"), ColorGray),
		colorize("  ·  ", ColorGray),
		colorize(i18n.T("welcome.footer.switch_model.cmd"), ColorGreen),
		colorize(" "+i18n.T("welcome.footer.switch_model.desc"), ColorGray),
	)
	fmt.Println()
	fmt.Println(lipgloss.PlaceHorizontal(anchor, lipgloss.Center, footer))
	fmt.Println()
}
