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
}

// printLogo exibe o novo logo do ChatCLI em ASCII art.
// printLogo exibe o novo logo do ChatCLI em ASCII art.
func printLogo() {
	// *** LOGO CORRIGIDO E VERIFICADO FINALMENTE ***
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

	fmt.Println(coloredLogo)
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

	width := 80               // largura externa total
	innerTitle := width - 2   // entre as bordas para o título
	innerContent := width - 4 // "  " + conteúdo + "  "

	title := "Você Sabia?"

	// topo
	fmt.Println(colorize("╭"+strings.Repeat("─", width-2)+"╮", ColorGray))

	// título centralizado (conta runas, ignora ANSI)
	tl := visibleLen(title)
	left := (innerTitle - tl) / 2
	right := innerTitle - tl - left
	fmt.Println(colorize("│", ColorGray) + strings.Repeat(" ", left) + title + strings.Repeat(" ", right) + colorize("│", ColorGray))

	// linha em branco
	fmt.Println(colorize("│", ColorGray) + strings.Repeat(" ", innerTitle) + colorize("│", ColorGray))

	// conteúdo alinhado à esquerda, com padding correto
	for _, line := range wrapStringWithColor(tip, innerContent) {
		pad := innerContent - visibleLen(line)
		fmt.Println(colorize("│", ColorGray) + " " + line + strings.Repeat(" ", pad) + " " + colorize("│", ColorGray))
	}

	// linha em branco + rodapé
	fmt.Println(colorize("│", ColorGray) + strings.Repeat(" ", innerTitle) + colorize("│", ColorGray))
	fmt.Println(colorize("╰"+strings.Repeat("─", width-2)+"╯", ColorGray))
}

// wrapStringWithColor quebra uma string que contém códigos de cor.
//func wrapStringWithColor(text string, maxWidth int) []string {
//	var lines []string
//	words := strings.Fields(text)
//	if len(words) == 0 {
//		return []string{}
//	}
//
//	var currentLine strings.Builder
//	currentLength := 0
//
//	for _, word := range words {
//		cleanWordLen := len(removeColorCodes(word))
//
//		if cleanWordLen > maxWidth {
//			if currentLength > 0 {
//				lines = append(lines, currentLine.String())
//				currentLine.Reset()
//				currentLength = 0
//			}
//			lines = append(lines, word)
//			continue
//		}
//
//		// Verifica se a palavra cabe na linha atual
//		if currentLength+1+cleanWordLen > maxWidth {
//			lines = append(lines, currentLine.String())
//			currentLine.Reset()
//			currentLength = 0
//		}
//
//		if currentLength > 0 {
//			currentLine.WriteString(" ")
//			currentLength++
//		}
//
//		currentLine.WriteString(word)
//		currentLength += cleanWordLen
//	}
//
//	if currentLine.Len() > 0 {
//		lines = append(lines, currentLine.String())
//	}
//
//	return lines
//}

// PrintWelcomeScreen exibe a tela de boas-vindas completa.
func (cli *ChatCLI) PrintWelcomeScreen() {
	printLogo()

	v, c, _ := version.GetBuildInfo()
	if v != "" && v != "dev" && v != "unknown" {
		versionStr := fmt.Sprintf("Versão: %s (commit: %s)", v, c)
		padding := (80 - len(versionStr)) / 2
		fmt.Printf("%s%s\n\n", strings.Repeat(" ", padding), colorize(versionStr, ColorGray))
	}

	printTipBox()

	footer := colorize("/help", ColorGreen) + " para todos os comandos  •  " + colorize("/exit", ColorGreen) + " para sair"
	separator := colorize(strings.Repeat("─", 80), ColorGray)

	fmt.Println()
	fmt.Println(footer)
	fmt.Println(separator)

	// Mensagem do modelo com cor verde fluorescente
	modelInfo := fmt.Sprintf("🤖 Você está conversando com %s (%s)", cli.Client.GetModelName(), cli.Provider)
	fmt.Println(colorize(modelInfo, ColorLime))
	fmt.Println()
}
