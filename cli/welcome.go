package cli

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/cli/agent"
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

// cardMaxWidth caps the inner width of welcome-screen cards (tip box,
// active-model card). The number was tuned around the original 87-col
// global constant and matches the layout the welcome screen was
// originally designed around. On wider terminals we keep the cards at
// this width and center them so the layout stays balanced; on narrower
// terminals the cards shrink to fit.
const cardMaxWidth = 87

// screenWidth reports the live terminal width — the surface we center
// the welcome content over. Falls back to cardMaxWidth when stdout is
// not a TTY (CI / piped runs) so the layout still looks sane outside
// an interactive shell. Reads at call time so a window resize between
// PrintWelcomeScreen invocations is picked up automatically.
func screenWidth() int {
	w := agent.TerminalWidth()
	if w <= 0 {
		return cardMaxWidth
	}
	return w
}

// cardWidth returns the inner card width clamped to [40, cardMaxWidth].
// Used by the tip box and active-model card so they keep a comfortable
// reading width regardless of the terminal size, while remaining
// centered on screenWidth().
func cardWidth() int {
	w := screenWidth() - 2
	if w > cardMaxWidth {
		return cardMaxWidth
	}
	if w < 40 {
		return 40
	}
	return w
}

// printLogo exibe o novo logo do ChatCLI em ASCII art, centralizado na
// largura real do terminal. Quando o terminal é mais estreito que o
// logo, imprimimos sem padding (o terminal cuida do wrap, melhor que
// truncarmos cegamente).
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

	target := screenWidth()
	for _, line := range strings.Split(coloredLogo, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		visible := visibleLen(line)
		if visible < target {
			left := (target - visible) / 2
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

// tipBoxBorderStyle is the rounded gray border used for the welcome
// tip box and the active-model card. Defined once so future palette
// changes touch one spot. lipgloss.Color("8") maps to the terminal's
// "bright black" ANSI slot — same color the rest of the welcome
// screen uses for chrome, so it stays themable.
var tipBoxBorderStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("8"))

// printTipBox renders a centered "Did you know?" tip card. lipgloss
// owns the border drawing and width math so a future change to the
// box style (different border, padding tweak, color) is a one-liner
// instead of a fistful of strings.Repeat calls.
func printTipBox() {
	tipKey := tipKeys[rand.Intn(len(tipKeys))] //#nosec G404 -- non-cryptographic: picks a welcome-screen tip
	tip := i18n.T(tipKey)
	title := i18n.T("welcome.tip.title")

	card := cardWidth()
	// Inner content width = card width − 2 borders − 2 of padding.
	innerWidth := card - 4
	if innerWidth < 20 {
		innerWidth = 20
	}

	// Title line: " ─── Did you know? ─── " centered above the body.
	// We bake the title into the body so it appears INSIDE the box at
	// the top, separated from the tip by a blank line. lipgloss's
	// default border doesn't natively accept a "title within the top
	// edge" yet, so this is the cleanest workaround.
	titleLine := lipgloss.NewStyle().
		Width(innerWidth).
		Align(lipgloss.Center).
		Bold(true).
		Foreground(lipgloss.Color("15")).
		Render(title)

	// Wrap the tip body to the inner width using the existing visible-
	// length-aware helper. lipgloss's word-wrap is also ANSI-aware but
	// our wrapStringWithColor already handles the edge cases (color
	// codes mid-word) so we reuse it.
	wrapped := strings.Join(wrapStringWithColor(tip, innerWidth), "\n")
	body := lipgloss.NewStyle().
		Width(innerWidth).
		Align(lipgloss.Center).
		Foreground(lipgloss.Color("7")).
		Render(wrapped)

	rendered := tipBoxBorderStyle.
		Padding(1, 1).
		Render(titleLine + "\n\n" + body)

	// Center the rendered card on the live terminal width so the
	// overall welcome layout stays balanced regardless of how the
	// user has sized their terminal.
	fmt.Println(lipgloss.PlaceHorizontal(screenWidth(), lipgloss.Center, rendered))
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

	target := screenWidth()

	v, c, _ := version.GetBuildInfo()
	if v != "" && v != "dev" && v != "unknown" {
		versionStr := i18n.T("version.label", v, c)
		fmt.Println(lipgloss.PlaceHorizontal(target, lipgloss.Center,
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
	modelCard := tipBoxBorderStyle.
		Padding(0, 2).
		Render(modelLine)
	fmt.Println(lipgloss.PlaceHorizontal(target, lipgloss.Center, modelCard))

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
	fmt.Println(lipgloss.PlaceHorizontal(target, lipgloss.Center, footer))
	fmt.Println()
}
