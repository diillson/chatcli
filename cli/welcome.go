package cli

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/diillson/chatcli/version"
)

// Dicas para serem exibidas aleatoriamente
var tips = []string{
	"Use " + colorize("@file <caminho>", ColorCyan) + " para adicionar o conteúdo de um arquivo ao contexto.",
	"Use " + colorize("@git", ColorCyan) + " para incluir o status e os commits recentes do seu repositório.",
	"Precisa executar um comando? Use " + colorize("@command <seu_comando>", ColorCyan) + ".",
	"Alterne entre provedores de IA a qualquer momento com o comando " + colorize("/switch", ColorGreen) + ".",
	"Limpe o histórico e comece uma nova conversa com " + colorize("/newsession", ColorGreen) + ".",
	"Verifique sua configuração atual (sem segredos!) com o comando " + colorize("/config", ColorGreen) + ".",
	"Pressione " + colorize("Ctrl+C", ColorCyan) + " uma vez para cancelar uma resposta da IA sem sair do chat.",
	"Use o modo agente com " + colorize("/agent <tarefa>", ColorGreen) + " para que a IA execute comandos por você.",
	"No Modo Agente, use " + colorize("p", ColorCyan) + " para alternar entre Visão COMPACTA e COMPLETA.",
	"No Modo Agente, use " + colorize("vN", ColorCyan) + " para abrir a saída completa no pager e " + colorize("wN", ColorCyan) + " para salvar em arquivo.",
	"O 'Último Resultado' do Modo Agente aparece sempre no rodapé, sem precisar rolar a tela.",
}

const screenWidth = 85 // largura global para tudo

// printLogo exibe o novo logo do ChatCLI em ASCII art.
func printLogo() {
	logo := `
       ██████╗ ██╗  ██╗ █████╗ ████████╗ ██████╗██╗     ██╗
      ██╔════╝ ██║  ██║██╔══██╗╚══██╔══╝██╔════╝██║     ██║
      ██║      ███████║███████║   ██║   ██║     ██║     ██║
      ██║      ██╔══██║██╔══██║   ██║   ██║     ██║     ██║
      ╚██████╗ ██║  ██║██║  ██║   ██║   ╚██████╗███████╗██║
       ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝   ╚═════╝╚══════╝╚═╝
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
	return utf8.RuneCountInString(removeColorCodes(s))
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

// --- caixa de dica (sem argumentos) ---
func printTipBox() {
	tip := tips[rand.Intn(len(tips))]

	width := screenWidth
	innerContent := width - 4
	title := "Você Sabia?"

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

// PrintWelcomeScreen exibe a tela de boas-vindas completa.
func (cli *ChatCLI) PrintWelcomeScreen() {
	printLogo()

	v, c, _ := version.GetBuildInfo()
	if v != "" && v != "dev" && v != "unknown" {
		versionStr := fmt.Sprintf("Versão: %s (commit: %s)", v, c)
		padding := (screenWidth - len(versionStr)) / 2
		fmt.Printf("%s%s\n\n", strings.Repeat(" ", padding), colorize(versionStr, ColorGray))
	}

	printTipBox()

	footer := colorize("/help", ColorGreen) +
		colorize(" para todos os comandos  •  ", ColorGray) +
		colorize("/exit", ColorGreen) +
		colorize(" para sair  •  ", ColorGray) +
		colorize("/switch --model", ColorGreen) +
		colorize(" trocar modelo", ColorGray)

	separator := colorize(strings.Repeat("━", screenWidth), ColorGray)

	fmt.Println()
	// footer alinhado à esquerda
	fmt.Println(footer)
	fmt.Println(separator)

	// model info alinhado à esquerda
	modelInfo := fmt.Sprintf("🤖 Você está conversando com %s (%s)", cli.Client.GetModelName(), cli.Provider)
	fmt.Println(colorize(modelInfo, ColorLime))
	fmt.Println()
}
