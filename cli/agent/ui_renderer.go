/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package agent

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
	"golang.org/x/term"
)

// ANSI Color codes exportados
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorLime   = "\033[92m"
	ColorCyan   = "\033[36m"
	ColorGray   = "\033[90m"
	ColorPurple = "\033[35m"
	ColorBold   = "\033[1m"
	ColorYellow = "\033[33m"
)

// UIRenderer gerencia a renderização da interface do modo agente
type UIRenderer struct {
	logger              *zap.Logger
	skipClearOnNextDraw bool
}

// NewUIRenderer cria uma nova instância do renderizador de UI
func NewUIRenderer(logger *zap.Logger) *UIRenderer {
	return &UIRenderer{
		logger:              logger,
		skipClearOnNextDraw: true,
	}
}

// Colorize aplica cores ANSI (exportada com C maiúsculo)
func (r *UIRenderer) Colorize(text string, color string) string {
	return fmt.Sprintf("%s%s%s", color, text, ColorReset)
}

// ClearScreen limpa a tela (se permitido)
func (r *UIRenderer) ClearScreen() {
	if r.skipClearOnNextDraw {
		r.skipClearOnNextDraw = false
		return
	}
	fmt.Print("\033[2J\033[H")
}

// SetSkipClearOnNextDraw define se o próximo clear deve ser pulado
func (r *UIRenderer) SetSkipClearOnNextDraw(skip bool) {
	r.skipClearOnNextDraw = skip
}

// ShowInPager abre texto em pager (less/more)
func (r *UIRenderer) ShowInPager(text string) error {
	pager := "less"
	args := []string{"-R"}
	if runtime.GOOS == "windows" {
		pager = "more"
		args = nil
	}
	cmd := exec.Command(pager, args...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// PrintPlanCompact imprime plano em formato compacto
func (r *UIRenderer) PrintPlanCompact(blocks []CommandBlock, outputs []*CommandOutput) {
	fmt.Println(r.Colorize(i18n.T("agent.plan.compact_view"), ColorLime+ColorBold))
	for i, b := range blocks {
		status := "⏳"
		if i < len(outputs) && outputs[i] != nil {
			if strings.TrimSpace(outputs[i].ErrorMsg) == "" {
				status = "✅"
			} else {
				status = "❌"
			}
		}
		title := b.Description
		if title == "" {
			title = i18n.T("agent.plan.default_description")
		}

		firstLine := ""
		if len(b.Commands) > 0 {
			firstLine = strings.Split(b.Commands[0], "\n")[0]
		}
		fmt.Printf("  %s #%d: %s — %s\n",
			status, i+1, title, r.Colorize(firstLine, ColorGray))
	}
	fmt.Println()
}

// PrintPlanFull imprime plano em formato completo
func (r *UIRenderer) PrintPlanFull(blocks []CommandBlock, outputs []*CommandOutput, validator *CommandValidator) {
	fmt.Println(r.Colorize(i18n.T("agent.plan.full_view"), ColorLime+ColorBold))

	for i, b := range blocks {
		status := i18n.T("agent.plan.status.pending")
		statusColor := ColorGray
		if i < len(outputs) && outputs[i] != nil {
			if strings.TrimSpace(outputs[i].ErrorMsg) == "" {
				status = i18n.T("agent.plan.status.ok")
				statusColor = ColorGreen
			} else {
				status = i18n.T("agent.plan.status.error")
				statusColor = ColorYellow
			}
		}

		title := b.Description
		if title == "" {
			title = i18n.T("agent.plan.default_description")
		}
		danger := ""
		if r.isBlockDangerous(b, validator) {
			danger = r.Colorize(i18n.T("agent.plan.risk.dangerous"), ColorYellow)
		} else {
			danger = r.Colorize(i18n.T("agent.plan.risk.safe"), ColorGray)
		}

		fmt.Printf("\n%s\n", r.Colorize(fmt.Sprintf(i18n.T("agent.plan.command_header"), i+1, title), ColorPurple+ColorBold))
		fmt.Printf("    %s %s\n", r.Colorize(i18n.T("agent.plan.field.type"), ColorGray), b.Language)
		fmt.Printf("    %s %s\n", r.Colorize(i18n.T("agent.plan.field.risk"), ColorGray), danger)
		fmt.Printf("    %s %s\n", r.Colorize(i18n.T("agent.plan.field.status"), ColorGray), r.Colorize(status, statusColor))

		fmt.Println(r.Colorize("    "+i18n.T("agent.plan.field.code"), ColorGray))
		for idx, cmd := range b.Commands {
			if len(b.Commands) > 1 {
				fmt.Print(r.Colorize(fmt.Sprintf("      "+i18n.T("agent.plan.command_separator")+"\n", idx+1, len(b.Commands)), ColorGray))
			}
			prefix := ""
			if b.Language == "shell" || b.Language == "bash" || b.Language == "sh" {
				prefix = "$ "
			}
			for _, ln := range strings.Split(cmd, "\n") {
				fmt.Printf(r.Colorize("      %s%s\n", ColorCyan), prefix, ln)
			}
			if idx < len(b.Commands)-1 {
				fmt.Println(r.Colorize("      ─────────────────────────────────────────", ColorGray))
			}
		}
	}
	fmt.Println()
}

// isBlockDangerous verifica se algum comando do bloco é perigoso
func (r *UIRenderer) isBlockDangerous(b CommandBlock, validator *CommandValidator) bool {
	for _, c := range b.Commands {
		if validator.IsDangerous(c) {
			return true
		}
	}
	return false
}

// PrintLastResult imprime o último resultado
func (r *UIRenderer) PrintLastResult(outputs []*CommandOutput, lastIdx int) {
	if lastIdx < 0 || lastIdx >= len(outputs) || outputs[lastIdx] == nil {
		return
	}
	fmt.Println(r.Colorize(i18n.T("agent.last_result.header"), ColorLime+ColorBold))

	out := outputs[lastIdx].Output
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	max := 30
	if len(lines) > max {
		preview := strings.Join(lines[:max], "\n") + "\n...\n"
		fmt.Print(preview)
	} else {
		fmt.Println(out)
	}

	tipsMessage := i18n.T("agent.last_result.tips", lastIdx+1, lastIdx+1)
	fmt.Printf("\n%s\n", tipsMessage)
}

// PrintHeader imprime o cabeçalho do modo agente
func (r *UIRenderer) PrintHeader() {
	fmt.Println("\n" + r.Colorize(" "+strings.Repeat("━", 58), ColorGray))
	fmt.Println(r.Colorize(i18n.T("agent.header.title"), ColorLime+ColorBold))
	fmt.Println(r.Colorize(" "+strings.Repeat("━", 58), ColorGray))
	fmt.Println(r.Colorize(i18n.T("agent.header.description"), ColorGray))
}

// PrintMenu imprime o menu de opções
func (r *UIRenderer) PrintMenu() {
	fmt.Println("\n" + r.Colorize(strings.Repeat("-", 60), ColorGray))
	fmt.Println(r.Colorize(i18n.T("agent.menu.header"), ColorLime+ColorBold))
	fmt.Println(r.Colorize(strings.Repeat("-", 60), ColorGray))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "[1..N]"), ColorYellow), i18n.T("agent.menu.exec_n"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "a"), ColorYellow), i18n.T("agent.menu.exec_all"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "eN"), ColorYellow), i18n.T("agent.menu.edit"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "tN"), ColorYellow), i18n.T("agent.menu.dry_run"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "cN"), ColorYellow), i18n.T("agent.menu.continue"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "pcN"), ColorYellow), i18n.T("agent.menu.pre_context"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "acN"), ColorYellow), i18n.T("agent.menu.post_context"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "vN"), ColorYellow), i18n.T("agent.menu.view"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "wN"), ColorYellow), i18n.T("agent.menu.save"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "p"), ColorYellow), i18n.T("agent.menu.toggle_plan"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "r"), ColorYellow), i18n.T("agent.menu.redraw"))
	fmt.Printf("  %s: %s\n", r.Colorize(fmt.Sprintf("%-6s", "q"), ColorYellow), i18n.T("agent.menu.quit"))
	fmt.Println(r.Colorize(strings.Repeat("-", 60), ColorGray))
}

// PrintPrompt imprime o prompt de entrada
func (r *UIRenderer) PrintPrompt() string {
	return r.Colorize(i18n.T("agent.prompt.choice"), ColorLime)
}

// VisibleLen calcula comprimento visível (sem ANSI codes) - EXPORTADA
func VisibleLen(s string) int {
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	cleaned := ansiRe.ReplaceAllString(s, "")
	return len(cleaned)
}

// RenderTimelineEvent desenha um "card" estilizado ajustado à largura do terminal
func (r *UIRenderer) RenderTimelineEvent(icon, title, content, color string) {
	// 1. Obter largura do terminal
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width <= 0 {
		width = 80 // Fallback seguro
	}

	// Definir margens
	// "│  " ocupa 3 colunas visuais
	const borderPrefixLen = 3
	// Largura útil para o texto = Largura total - borda esq - borda dir (aprox)
	textWidth := width - borderPrefixLen - 2
	if textWidth < 20 {
		textWidth = 20
	} // Segurança para telas muito pequenas

	// Limpa formatação anterior
	fmt.Println()

	// Cabeçalho do Card
	header := fmt.Sprintf("%s %s", icon, title)
	fmt.Println(r.Colorize("╭── "+header, color+ColorBold))

	// Prefixo lateral colorido
	prefix := r.Colorize("│", color) + "  "

	// 2. Quebrar o texto manualmente para caber na caixa
	wrappedLines := wrapText(content, textWidth)

	// 3. Imprimir cada linha com a borda
	for _, line := range wrappedLines {
		fmt.Println(prefix + line)
	}

	// Rodapé do Card (ajustado à largura se quiser, ou fixo)
	// Vamos fazer uma linha que vai até o final da tela para ficar bonito
	footerLen := width - 2 // -2 para compensar a curva
	if footerLen < 0 {
		footerLen = 10
	}
	footer := "╰" + strings.Repeat("─", footerLen)

	fmt.Println(r.Colorize(footer, color))
}

// wrapText quebra o texto em linhas que não excedem o limite
func wrapText(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}

	var finalLines []string

	// Preservar quebras de linha originais (parágrafos)
	paragraphs := strings.Split(text, "\n")

	for _, p := range paragraphs {
		// Se a linha for vazia, mantém vazia
		if strings.TrimSpace(p) == "" {
			finalLines = append(finalLines, "")
			continue
		}

		words := strings.Fields(p)
		if len(words) == 0 {
			continue
		}

		currentLine := words[0]
		for _, word := range words[1:] {
			// +1 é o espaço
			if len(currentLine)+1+len(word) <= limit {
				currentLine += " " + word
			} else {
				finalLines = append(finalLines, currentLine)
				currentLine = word
			}
		}
		finalLines = append(finalLines, currentLine)
	}

	return finalLines
}

// RenderThinking exibe o pensamento da IA
func (r *UIRenderer) RenderThinking(thought string) {
	if strings.TrimSpace(thought) == "" {
		return
	}
	// Usa cor cinza/ciano para pensamento
	r.RenderTimelineEvent("🧠", "RACIOCÍNIO", thought, ColorCyan)
}

// RenderToolCall exibe a chamada da ferramenta de forma limpa (escondendo Base64 e sujeira HTML)
func (r *UIRenderer) RenderToolCall(toolName, rawArgs string) {
	// 1. Decodificar HTML entities (&quot; -> ", &#10; -> \n)
	cleanArgs := html.UnescapeString(rawArgs)

	// 2. Remover quebras de linha com barra invertida (visualização em linha única)
	// Isso faz o box ficar igual ao comando que será realmente executado
	re := regexp.MustCompile(`\\\s*[\r\n]+`)
	cleanArgs = re.ReplaceAllString(cleanArgs, " ")

	// 3. Remove espaços extras gerados pela junção das linhas
	spaceRe := regexp.MustCompile(`\s+`)
	cleanArgs = spaceRe.ReplaceAllString(strings.TrimSpace(cleanArgs), " ")

	// 4. Limpar argumentos específicos (esconder base64 gigante) - Função existente
	displayArgs := cleanArgsForDisplay(cleanArgs)

	content := fmt.Sprintf("Ferramenta: %s\nArgs: %s",
		r.Colorize(toolName, ColorYellow+ColorBold),
		displayArgs)

	r.RenderTimelineEvent("🔨", "EXECUTANDO AÇÃO", content, ColorYellow)
}

// RenderToolResult exibe o resultado da execução
func (r *UIRenderer) RenderToolResult(output string, isError bool) {
	icon := "✅"
	title := "SUCESSO"
	color := ColorGreen

	if isError {
		icon = "❌"
		title = "FALHA NA EXECUÇÃO"
		color = ColorPurple // Vermelho seria melhor, mas usando Purple da sua lib
	}

	// Truncar output muito grande para não poluir a timeline visualmente
	// O agente recebe tudo, mas o humano vê um resumo se for gigante
	displayOutput := output
	if len(output) > 2000 {
		displayOutput = output[:2000] + "\n... [saída truncada visualmente, agente recebeu tudo] ..."
	}

	r.RenderTimelineEvent(icon, title, displayOutput, color)
}

// Helper para limpar argumentos visuais
func cleanArgsForDisplay(args string) string {
	// Esconder conteúdo base64 longo
	re := regexp.MustCompile(`(content|search|replace)=['"]?([A-Za-z0-9+/=]{50,})['"]?`)
	return re.ReplaceAllString(args, "$1=\"[DADOS_BASE64_OCULTOS]\"")
}
