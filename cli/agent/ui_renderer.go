/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package agent

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/mattn/go-runewidth"
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

// imprime a linha COM A BARRA LATERAL para parecer que está dentro
func (r *UIRenderer) StreamOutput(line string) {
	// A borda lateral tem que ter a mesma cor do Header (ColorPurple geralmente)
	prefix := r.Colorize("│", ColorPurple) + "  "

	// Lógica de ícones (mantive a sua lógica de cores)
	if strings.HasPrefix(line, "ERR: ") {
		cleanLine := strings.TrimPrefix(line, "ERR: ")
		fmt.Println(r.Colorize(prefix+"⚠️  "+cleanLine, ColorYellow))
	} else {
		icon := "  " // indentação padrão
		//lower := strings.ToLower(line)
		//
		//if strings.Contains(lower, "sucesso") || strings.Contains(line, "✅") {
		//	icon = " ✅ "
		//} else if strings.Contains(lower, "erro") || strings.Contains(lower, "falha") {
		//	icon = " ❌ "
		//} else if strings.HasPrefix(strings.TrimSpace(line), "$") {
		//	icon = "💲 "
		//}

		// Imprime: │  ICON Texto...
		fmt.Println(prefix + r.Colorize(icon+line, ColorGray))
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
		title := strings.TrimSpace(b.Description)
		if title == "" {
			title = i18n.T("agent.plan.default_description")
		}

		firstLine := ""
		if len(b.Commands) > 0 {
			firstLine = strings.Split(b.Commands[0], "\n")[0]
			firstLine = strings.TrimSpace(firstLine)
		}

		cleanCmd := strings.TrimSpace(strings.TrimPrefix(firstLine, "#"))

		// Compara ignorando case
		isRedundant := strings.EqualFold(title, cleanCmd) || title == firstLine

		if isRedundant {
			// Se for redundante, mostra destacado em CIANO e apenas uma vez
			// Normalmente mostramos o 'firstLine' original (com # se tiver) para fidelidade
			fmt.Printf("  %s #%d: %s\n",
				status, i+1, r.Colorize(firstLine, ColorCyan))
		} else {
			// Se for diferente, descritivo em BRANCO (padrão) e comando em CINZA
			fmt.Printf("  %s #%d: %s — %s\n",
				status, i+1, title, r.Colorize(firstLine, ColorGray))
		}
	}
	fmt.Println()
}

// PrintPlanFull imprime plano em formato completo
func (r *UIRenderer) PrintPlanFull(blocks []CommandBlock, outputs []*CommandOutput, validator *CommandValidator) {
	// Título fixo
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

		// --- CORREÇÃO DO PROBLEMA "MISSING" AQUI ---
		// Passamos os argumentos (i+1, title) DIRETO para o i18n.T
		headerText := i18n.T("agent.plan.command_header", i+1, title)

		fmt.Printf("\n%s\n", r.Colorize(headerText, ColorPurple+ColorBold))
		// ------------------------------------------

		fmt.Printf("    %s %s\n", r.Colorize(i18n.T("agent.plan.field.type"), ColorGray), b.Language)
		fmt.Printf("    %s %s\n", r.Colorize(i18n.T("agent.plan.field.risk"), ColorGray), danger)
		fmt.Printf("    %s %s\n", r.Colorize(i18n.T("agent.plan.field.status"), ColorGray), r.Colorize(status, statusColor))

		fmt.Println(r.Colorize("    "+i18n.T("agent.plan.field.code"), ColorGray))
		for idx, cmd := range b.Commands {
			if len(b.Commands) > 1 {
				// Correção aqui também para o separador (ex: 1/3)
				sepText := i18n.T("agent.plan.command_separator", idx+1, len(b.Commands))
				fmt.Print(r.Colorize("      "+sepText+"\n", ColorGray))
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

// VisibleLen calcula comprimento visível em colunas do terminal (sem ANSI codes).
// Usa runewidth para tratar emojis e caracteres wide corretamente.
func VisibleLen(s string) int {
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	cleaned := ansiRe.ReplaceAllString(s, "")
	return runewidth.StringWidth(cleaned)
}

// RenderTimelineEvent desenha um "card" estilizado ajustado à largura do terminal
func (r *UIRenderer) RenderTimelineEvent(icon, title, content, color string) {
	// 1. Obter largura do terminal
	width, _, err := term.GetSize(int(os.Stdout.Fd())) //#nosec G115 -- value bounded by domain
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

// RenderMarkdownTimelineEvent renderiza markdown (já convertido para ANSI fora) dentro do card.
// Ele só delega para RenderTimelineEvent, mas existe para explicitar intenção e padronizar chamadas.
func (r *UIRenderer) RenderMarkdownTimelineEvent(icon, title, renderedMarkdownANSI, color string) {
	if strings.TrimSpace(renderedMarkdownANSI) == "" {
		return
	}
	r.RenderTimelineEvent(icon, title, renderedMarkdownANSI, color)
}

// wrapText quebra o texto em linhas que não excedem o limite.
// Versão production-ready:
// - Preserva quebras de linha originais
// - Faz word-wrap por largura visível (ignora ANSI)
// - Não destrói formatação do markdown renderizado (ANSI + linhas)
func wrapText(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}

	var finalLines []string
	paragraphs := strings.Split(text, "\n")

	for _, p := range paragraphs {
		// Preserva linha vazia
		if p == "" {
			finalLines = append(finalLines, "")
			continue
		}

		// Word wrap baseado em largura visível
		words := strings.Fields(p)
		if len(words) == 0 {
			finalLines = append(finalLines, "")
			continue
		}

		var line strings.Builder
		curLen := 0

		flushLine := func() {
			finalLines = append(finalLines, line.String())
			line.Reset()
			curLen = 0
		}

		for _, w := range words {
			wLen := VisibleLen(w)
			if curLen == 0 {
				line.WriteString(w)
				curLen = wLen
				continue
			}

			// +1 espaço
			if curLen+1+wLen <= limit {
				line.WriteByte(' ')
				line.WriteString(w)
				curLen += 1 + wLen
				continue
			}

			// Se a palavra isolada estoura, quebra "na marra" por runas,
			// respeitando ANSI (aproximação: fatia por bytes, mas valida pela VisibleLen)
			flushLine()

			if wLen <= limit {
				line.WriteString(w)
				curLen = wLen
				continue
			}

			// quebra a palavra grande em pedaços
			rest := w
			for VisibleLen(rest) > limit {
				cut := 1
				for cut < len(rest) && VisibleLen(rest[:cut]) < limit {
					cut++
				}
				// cut passou, volta 1
				if cut > 1 {
					cut--
				}
				finalLines = append(finalLines, rest[:cut])
				rest = rest[cut:]
			}
			line.WriteString(rest)
			curLen = VisibleLen(rest)
		}

		if line.Len() > 0 {
			finalLines = append(finalLines, line.String())
		}
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

	r.RenderTimelineEvent("🔨", "EXECUTAR AÇÃO", content, ColorYellow)
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

// RenderToolCallMinimal exibe a chamada de ferramenta em modo compacto
func (r *UIRenderer) RenderToolCallMinimal(toolName, rawArgs string, current, total int) {
	displayArgs := html.UnescapeString(rawArgs)
	reBackslashNewline := regexp.MustCompile(`\\\s*[\r\n]+`)
	displayArgs = reBackslashNewline.ReplaceAllString(displayArgs, " ")
	spaceRe := regexp.MustCompile(`\s+`)
	displayArgs = spaceRe.ReplaceAllString(strings.TrimSpace(displayArgs), " ")
	displayArgs = cleanArgsForDisplay(displayArgs)

	title := fmt.Sprintf("AÇÃO %d/%d", current, total)
	content := fmt.Sprintf("%s %s",
		r.Colorize(toolName, ColorYellow+ColorBold),
		r.Colorize(displayArgs, ColorCyan))

	r.RenderTimelineEvent("⚙️", title, content, ColorYellow)
}

// RenderToolResultMinimal exibe o resultado em modo compacto
func (r *UIRenderer) RenderToolResultMinimal(output string, isError bool) {
	icon := "✅"
	title := "OK"
	color := ColorGreen

	if isError {
		icon = "❌"
		title = "ERRO"
		color = ColorPurple
	}

	display := strings.TrimSpace(output)
	if idx := strings.Index(display, "\n"); idx >= 0 {
		display = display[:idx]
	}
	if len(display) > 240 {
		display = display[:240] + "..."
	}
	if display == "" {
		display = "-"
	}

	r.RenderTimelineEvent(icon, title, display, color)
}

// Helper para limpar argumentos visuais
func cleanArgsForDisplay(args string) string {
	// Esconder conteúdo base64 longo
	re := regexp.MustCompile(`(content|search|replace)=['"]?([A-Za-z0-9+/=]{50,})['"]?`)
	return re.ReplaceAllString(args, "$1=\"[DADOS_BASE64_OCULTOS]\"")
}

// RenderBatchHeader exibe um cabeçalho indicando o início de um lote
func (r *UIRenderer) RenderBatchHeader(totalActions int) {
	width, _, _ := term.GetSize(int(os.Stdout.Fd())) //#nosec G115 -- value bounded by domain
	if width <= 0 {
		width = 80
	}

	msg := fmt.Sprintf(" 📦 INICIANDO EXECUÇÃO EM LOTE: %d AÇÕES ", totalActions)
	line := strings.Repeat("═", width)

	// Centralizar visualmente
	padding := (width - VisibleLen(msg)) / 2
	if padding < 0 {
		padding = 0
	}

	fmt.Println()
	fmt.Println(r.Colorize(line, ColorPurple))
	fmt.Printf("%s%s\n", strings.Repeat(" ", padding), r.Colorize(msg, ColorPurple+ColorBold))
	fmt.Println(r.Colorize(line, ColorPurple))
}

// RenderToolCallWithProgress exibe a chamada da ferramenta em formato de CARD (Box),
// limpando barras invertidas visuais e mostrando o progresso.
func (r *UIRenderer) RenderToolCallWithProgress(toolName, rawArgs string, current, total int) {
	// 1. Limpeza visual (apenas para exibição, não afeta execução)

	// Decodifica HTML (&quot; -> ")
	displayArgs := html.UnescapeString(rawArgs)

	// Remove a sequência "barra invertida + quebra de linha" que polui o visual
	// Ex: "echo hello \ \n world" vira "echo hello   world"
	reBackslashNewline := regexp.MustCompile(`\\\s*[\r\n]+`)
	displayArgs = reBackslashNewline.ReplaceAllString(displayArgs, " ")

	// Remove espaços extras gerados pela junção
	spaceRe := regexp.MustCompile(`\s+`)
	displayArgs = spaceRe.ReplaceAllString(strings.TrimSpace(displayArgs), " ")

	// Aplica a limpeza de segurança (esconder base64 gigante)
	displayArgs = cleanArgsForDisplay(displayArgs)

	// 2. Monta o conteúdo do Card
	// Formato:
	// Ferramenta: @coder
	// Args: ...
	content := fmt.Sprintf("Ferramenta: %s\nComando: %s",
		r.Colorize(toolName, ColorYellow+ColorBold),
		r.Colorize(displayArgs, ColorCyan))

	// 3. Título com progresso
	title := fmt.Sprintf("EXECUTAR AÇÃO (%d/%d)", current, total)

	// 4. Renderiza usando o sistema de cards existente
	r.RenderTimelineEvent("🔨", title, content, ColorYellow)
}

// RenderBatchSummary exibe o resultado final do lote
func (r *UIRenderer) RenderBatchSummary(successCount, total int, hasError bool) {
	fmt.Println()
	if hasError {
		msg := fmt.Sprintf(" ⚠️ LOTE INTERROMPIDO: %d de %d ações executadas com sucesso.", successCount, total)
		fmt.Println(r.Colorize(msg, ColorYellow))
	} else {
		msg := fmt.Sprintf(" ✅ LOTE CONCLUÍDO: Todas as %d ações foram executadas.", total)
		fmt.Println(r.Colorize(msg, ColorGreen))
	}
	fmt.Println(r.Colorize(strings.Repeat("─", 60), ColorGray))
}

// desenha APENAS o cabeçalho do card
func (r *UIRenderer) RenderStreamBoxStart(icon, title, color string) {

	header := fmt.Sprintf("%s %s", icon, title)

	fmt.Println() // Pula linha inicial
	// Desenha a curva superior: ╭── ICON TITULO
	fmt.Println(r.Colorize("╭── "+header, color+ColorBold))
}

// desenha APENAS o rodapé
func (r *UIRenderer) RenderStreamBoxEnd(color string) {
	width, _, err := term.GetSize(int(os.Stdout.Fd())) //#nosec G115 -- value bounded by domain
	if err != nil || width <= 0 {
		width = 80
	}

	// Calcula tamanho da linha de baixo: ╰──────...
	footerLen := width - 2
	if footerLen < 0 {
		footerLen = 10
	}

	footer := "╰" + strings.Repeat("─", footerLen)
	fmt.Println(r.Colorize(footer, color))
}

// ─── Compact display (aru-style) ────────────────────────────────────────────
// All compact methods render single inline lines instead of card boxes.
// This dramatically reduces visual noise in coder mode.

// CompactToolStart renders a tool call start in compact format:
//
//	↻ Read(main.go)
func (r *UIRenderer) CompactToolStart(toolLabel string) {
	fmt.Printf("  %s %s\n",
		r.Colorize("↻", ColorCyan),
		r.Colorize(toolLabel, ColorGray))
}

// CompactToolDone renders a completed tool call in compact format:
//
//	✓ Read(main.go) 1.2s
func (r *UIRenderer) CompactToolDone(toolLabel string, duration string, isError bool) {
	if isError {
		fmt.Printf("  %s %s %s\n",
			r.Colorize("✗", ColorYellow),
			r.Colorize(toolLabel, ColorGray),
			r.Colorize(duration, ColorYellow))
	} else {
		fmt.Printf("  %s %s %s\n",
			r.Colorize("✓", ColorGreen+ColorBold),
			r.Colorize(toolLabel, ColorGray),
			r.Colorize(duration, ColorCyan))
	}
}

// CompactLine renders a single inline status line (no card/box).
// Used for reasoning, explanations, errors, summaries in compact mode.
//
//	● PLANO  Ler main.go, modificar handler, atualizar testes
//	✗ ERRO   Arquivo não encontrado
//	✓ OK     2 arquivos modificados
func (r *UIRenderer) CompactLine(icon, label, text string, color string) {
	// Truncate text to single line
	text = strings.TrimSpace(text)
	if idx := strings.Index(text, "\n"); idx >= 0 {
		// Take first non-empty line
		first := strings.TrimSpace(text[:idx])
		rest := strings.TrimSpace(text[idx+1:])
		if first == "" && rest != "" {
			if idx2 := strings.Index(rest, "\n"); idx2 >= 0 {
				first = strings.TrimSpace(rest[:idx2])
			} else {
				first = rest
			}
		}
		text = first
	}
	if len(text) > 120 {
		text = text[:117] + "..."
	}

	fmt.Printf("  %s %s %s\n",
		r.Colorize(icon, color),
		r.Colorize(label, color+ColorBold),
		r.Colorize(text, ColorGray))
}

// CompactMultiLine renders a compact block: icon + label on first line,
// then indented content lines (max N lines). For reasoning/plan display.
//
//	● PLANO
//	  1. Ler main.go
//	  2. Modificar handleRequest
//	  3. Atualizar testes
func (r *UIRenderer) CompactMultiLine(icon, label, text string, color string, maxLines int) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	fmt.Printf("  %s %s\n",
		r.Colorize(icon, color),
		r.Colorize(label, color+ColorBold))

	lines := strings.Split(text, "\n")
	shown := 0
	for _, line := range lines {
		if shown >= maxLines {
			fmt.Printf("    %s\n", r.Colorize("...", ColorGray))
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fmt.Printf("    %s\n", r.Colorize(trimmed, ColorGray))
		shown++
	}
}

// CompactError renders an error inline.
//
//	✗ BLOCKED  Ação negada pelo usuário
func (r *UIRenderer) CompactError(msg string) {
	msg = strings.TrimSpace(msg)
	if len(msg) > 120 {
		msg = msg[:117] + "..."
	}
	fmt.Printf("  %s %s\n",
		r.Colorize("✗", ColorYellow),
		r.Colorize(msg, ColorYellow))
}

// CompactBatchSummary renders a one-line batch summary.
//
//	✓ 4/4 ações concluídas
//	✗ 2/4 ações concluídas (com erros)
func (r *UIRenderer) CompactBatchSummary(successCount, total int, hasError bool) {
	if hasError {
		fmt.Printf("\n  %s %s\n",
			r.Colorize("✗", ColorYellow),
			r.Colorize(fmt.Sprintf("%d/%d concluídas (com erros)", successCount, total), ColorYellow))
	} else if total > 1 {
		fmt.Printf("\n  %s %s\n",
			r.Colorize("✓", ColorGreen+ColorBold),
			r.Colorize(fmt.Sprintf("%d/%d concluídas", successCount, total), ColorGreen))
	}
}

// CompactToolLabel builds a compact label from a subcmd + args.
// Examples: "Read(main.go)", "Write(pkg/handler.go)", "Exec(go test ./...)", "Patch(3 edits)"
func CompactToolLabel(subcmd string, rawArgs string) string {
	// Map subcmd to display name
	displayNames := map[string]string{
		"read":        "Read",
		"write":       "Write",
		"patch":       "Patch",
		"tree":        "Tree",
		"search":      "Search",
		"exec":        "Exec",
		"test":        "Test",
		"rollback":    "Rollback",
		"clean":       "Clean",
		"git-status":  "GitStatus",
		"git-diff":    "GitDiff",
		"git-log":     "GitLog",
		"git-changed": "GitChanged",
		"git-branch":  "GitBranch",
		// Native tool names
		"read_file":      "Read",
		"write_file":     "Write",
		"patch_file":     "Patch",
		"list_directory": "Tree",
		"search_files":   "Search",
		"run_command":    "Exec",
		"run_tests":      "Test",
		"rollback_file":  "Rollback",
		"clean_backups":  "Clean",
		"git_status":     "GitStatus",
		"git_diff":       "GitDiff",
		"git_log":        "GitLog",
		"git_changed":    "GitChanged",
		"git_branch":     "GitBranch",
	}

	name := displayNames[subcmd]
	if name == "" {
		name = subcmd
	}

	// Extract primary argument for the label
	arg := extractPrimaryArg(subcmd, rawArgs)
	if arg == "" {
		return name
	}

	// Truncate long args
	if len(arg) > 60 {
		arg = arg[:57] + "..."
	}

	return fmt.Sprintf("%s(%s)", name, arg)
}

// extractPrimaryArg pulls the most relevant argument from raw args for compact display.
func extractPrimaryArg(subcmd string, rawArgs string) string {
	// Try JSON args first
	var jsonArgs map[string]interface{}
	if err := json.Unmarshal([]byte(rawArgs), &jsonArgs); err == nil {
		// Check for nested {"cmd":"...", "args":{...}} format
		if inner, ok := jsonArgs["args"]; ok {
			if innerMap, ok := inner.(map[string]interface{}); ok {
				jsonArgs = innerMap
			}
		}
		switch subcmd {
		case "read", "write", "patch", "read_file", "write_file", "patch_file", "rollback", "rollback_file":
			if f, ok := jsonArgs["file"].(string); ok {
				return f
			}
			if f, ok := jsonArgs["path"].(string); ok {
				return f
			}
		case "exec", "run_command":
			if c, ok := jsonArgs["cmd"].(string); ok {
				return c
			}
			if c, ok := jsonArgs["command"].(string); ok {
				return c
			}
		case "search", "search_files":
			if t, ok := jsonArgs["term"].(string); ok {
				return t
			}
		case "tree", "list_directory":
			if d, ok := jsonArgs["dir"].(string); ok {
				return d
			}
			return "."
		case "test", "run_tests":
			if c, ok := jsonArgs["cmd"].(string); ok {
				return c
			}
			if d, ok := jsonArgs["dir"].(string); ok {
				return d
			}
			return "auto"
		case "git-status", "git_status", "git-diff", "git_diff", "git-log", "git_log",
			"git-changed", "git_changed", "git-branch", "git_branch":
			if d, ok := jsonArgs["dir"].(string); ok {
				return d
			}
			if p, ok := jsonArgs["path"].(string); ok {
				return p
			}
			return "."
		}
		return ""
	}

	// CLI-style: try --file, --cmd, --term
	parts := strings.Fields(rawArgs)
	for i, p := range parts {
		switch p {
		case "--file", "-f", "--path":
			if i+1 < len(parts) {
				return parts[i+1]
			}
		case "--cmd", "--command":
			if i+1 < len(parts) {
				return parts[i+1]
			}
		case "--term", "--query":
			if i+1 < len(parts) {
				return parts[i+1]
			}
		case "--dir":
			if i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}

	return ""
}

// ─── Status phrases (rotating while thinking) ──────────────────────────────

// StatusPhrases are fun rotating messages shown while the LLM is thinking.
var StatusPhrases = []string{
	"Thinking...",
	"Analyzing...",
	"Processing...",
	"Reasoning...",
	"Planning...",
	"Exploring...",
	"Connecting dots...",
	"Crafting solution...",
	"Reading code...",
	"Mapping structure...",
}
