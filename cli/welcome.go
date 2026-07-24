package cli

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/diillson/chatcli/update"
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

// printTipBox renders the "Did you know?" tip in the sóbrio treatment: a
// titled rule opens the section and the tip sits on the standard two-space
// indent — no frame, hierarchy from typography alone.
func printTipBox() {
	tipKey := tipKeys[rand.Intn(len(tipKeys))] //#nosec G404 -- non-cryptographic: picks a welcome-screen tip
	tip := i18n.T(tipKey)
	anchor := screenWidth()

	fmt.Println(kit.RuleHeader(" "+kit.Bold(i18n.T("welcome.tip.title"), theme.RoleHeader)+" ", "", anchor))
	fmt.Println()
	for _, ln := range kit.WrapText(tip, anchor-2) {
		fmt.Println("  " + ln)
	}
}

// PrintWelcomeScreen exibe a tela de boas-vindas completa e traduzida no
// tratamento sóbrio: logo, versão dim, dica sob régua titulada, linha do
// modelo ativo com o marcador ◆ e rodapé de comandos — tudo no grid de
// indentação 2, sem molduras.
func (cli *ChatCLI) PrintWelcomeScreen() {
	printLogo()

	// OfflineReport enriquece o commit/data de builds go install a partir do
	// cache em disco da release — sem rede, o boot não espera nada. O refresh
	// do cache vencido roda em background no Start.
	rep := version.OfflineReport()
	current := rep.Current
	if v := current.Version; v != "" && v != "dev" && v != "unknown" {
		versionStr := i18n.T("version.label", v, current.CommitHash)
		fmt.Println("  " + colorize(versionStr, ColorGray))
	}
	// Primeiro boot após um auto-update em staging: anuncia a versão nova e
	// consome o registro. Fora disso, cache indicando release mais nova vira
	// o convite ao /update — suprimido quando a política é off.
	if rec, ok := update.ConsumeStagedRecord(current.Version); ok {
		fmt.Println("  " + colorize(i18n.T("welcome.update.applied", rec.To), ColorLime))
	} else if rep.NeedsUpdate && update.ResolveMode() != update.ModeOff {
		fmt.Println("  " + colorize(i18n.T("welcome.update.available", rep.Latest), ColorYellow))
	}
	fmt.Println()

	printTipBox()
	fmt.Println()

	// Active model as a plain marked line — same glyph vocabulary as the
	// compact timeline, no frame. Falls back to a "no model" state with a
	// hint when no provider is wired up.
	if cli.Client != nil {
		fmt.Println("  " +
			colorize("◆ ", ColorLime) +
			colorize(cli.Client.GetModelName(), ColorLime+ColorBold) +
			colorize(" · ", ColorGray) +
			colorize(cli.Provider, ColorGray))
	} else {
		fmt.Println("  " + colorize("◆ "+i18n.T("welcome.current_model", "(none)", "No provider"), ColorYellow))
		fmt.Println("  " + colorize(i18n.T("welcome.auth_hint"), ColorGray))
	}

	// Quick-command footer on the same grid.
	footer := colorize(i18n.T("welcome.footer.help.cmd"), ColorGreen) +
		colorize(" "+i18n.T("welcome.footer.help.desc"), ColorGray) +
		colorize("  ·  ", ColorGray) +
		colorize(i18n.T("welcome.footer.exit.cmd"), ColorGreen) +
		colorize(" "+i18n.T("welcome.footer.exit.desc"), ColorGray) +
		colorize("  ·  ", ColorGray) +
		colorize(i18n.T("welcome.footer.switch_model.cmd"), ColorGreen) +
		colorize(" "+i18n.T("welcome.footer.switch_model.desc"), ColorGray)
	fmt.Println()
	fmt.Println("  " + footer)
	fmt.Println()
}
