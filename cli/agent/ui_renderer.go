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

	"github.com/charmbracelet/lipgloss"
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
	ColorRed    = "\033[31m"
	ColorBlue   = "\033[34m"
)

// UIStyle selects how the renderer dispatches tool-call output across
// the timeline. The same enum drives both /coder and /agent paths; the
// env var CHATCLI_CODER_UI feeds it (legacy name kept on purpose — it
// now controls both modes, see DefaultUIStyleFromEnv).
type UIStyle int

const (
	// UIStyleFull renders every tool call inside a bordered card,
	// every reasoning block in its own panel. Best for supervised
	// /agent runs where the user reviews each action.
	UIStyleFull UIStyle = iota
	// UIStyleCompact renders one-line tool calls (↻/✓) so a long
	// /coder session with dozens of tool invocations stays scannable.
	UIStyleCompact
	// UIStyleMinimal renders boxed tool calls with truncated reasoning.
	// Sits between Full and Compact: lighter than Full, more context
	// than Compact.
	UIStyleMinimal
)

func (s UIStyle) String() string {
	switch s {
	case UIStyleCompact:
		return "compact"
	case UIStyleMinimal:
		return "minimal"
	default:
		return "full"
	}
}

// DefaultUIStyleFromEnv reads CHATCLI_CODER_UI and maps it to a UIStyle.
// Unset / "full" / "false" / "0" → Full. "compact" → Compact.
// "minimal" / "min" / "true" / "1" → Minimal. Legacy: "compact" used to
// imply Minimal in some call sites — kept as Compact here because that
// matches the explicit user intent of typing "compact".
func DefaultUIStyleFromEnv() UIStyle {
	val := strings.TrimSpace(strings.ToLower(os.Getenv("CHATCLI_CODER_UI")))
	switch val {
	case "compact":
		return UIStyleCompact
	case "minimal", "min", "true", "1":
		return UIStyleMinimal
	default:
		return UIStyleFull
	}
}

// UIRenderer gerencia a renderização da interface do modo agente
type UIRenderer struct {
	logger              *zap.Logger
	skipClearOnNextDraw bool
	style               UIStyle
}

// NewUIRenderer cria uma nova instância do renderizador de UI; o estilo
// é detectado a partir do ambiente. Para testes ou para forçar um
// estilo, use NewUIRendererWithStyle.
func NewUIRenderer(logger *zap.Logger) *UIRenderer {
	return NewUIRendererWithStyle(logger, DefaultUIStyleFromEnv())
}

// NewUIRendererWithStyle constrói um renderer com estilo explícito.
// Usado por testes e por callers que precisam forçar um estilo
// independentemente do ambiente (ex: relatórios offline).
func NewUIRendererWithStyle(logger *zap.Logger, style UIStyle) *UIRenderer {
	return &UIRenderer{
		logger:              logger,
		skipClearOnNextDraw: true,
		style:               style,
	}
}

// Style returns the resolved UI style for this renderer.
func (r *UIRenderer) Style() UIStyle { return r.style }

// IsFull reports whether tool calls render as full bordered cards.
func (r *UIRenderer) IsFull() bool { return r.style == UIStyleFull }

// IsCompact reports whether tool calls render as one-line ↻/✓ entries.
func (r *UIRenderer) IsCompact() bool { return r.style == UIStyleCompact }

// IsMinimal reports whether tool calls render as boxed-but-truncated cards.
func (r *UIRenderer) IsMinimal() bool { return r.style == UIStyleMinimal }

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

// PrintMenu renders the agent-mode action menu in three vertical
// columns grouped by intent (Execution / Edit & Context / View). The
// previous single-column layout produced a 12-row wall of `[1..N]: …`
// entries that scrolled past the user's tool output every turn — the
// columnar layout fits the same information in 6 rows while still
// keeping the key + description pairing readable.
//
// Columns are built with lipgloss.JoinHorizontal so a long description
// in any column does not push the other columns out of alignment.
func (r *UIRenderer) PrintMenu() {
	type entry struct{ key, desc string }

	execution := []entry{
		{"[1..N]", i18n.T("agent.menu.exec_n")},
		{"a", i18n.T("agent.menu.exec_all")},
		{"cN", i18n.T("agent.menu.continue")},
	}
	editContext := []entry{
		{"eN", i18n.T("agent.menu.edit")},
		{"tN", i18n.T("agent.menu.dry_run")},
		{"pcN", i18n.T("agent.menu.pre_context")},
		{"acN", i18n.T("agent.menu.post_context")},
	}
	view := []entry{
		{"vN", i18n.T("agent.menu.view")},
		{"wN", i18n.T("agent.menu.save")},
		{"p", i18n.T("agent.menu.toggle_plan")},
		{"r", i18n.T("agent.menu.redraw")},
		{"q", i18n.T("agent.menu.quit")},
	}

	// renderColumn turns a slice of {key, desc} entries into a single
	// multi-line string with the column header on top. Key column is
	// fixed width so descriptions stay vertically aligned.
	renderColumn := func(title string, entries []entry) string {
		const keyWidth = 6
		head := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")).Render(title)
		sep := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(strings.Repeat("─", lipgloss.Width(head)))
		rows := []string{head, sep}
		for _, e := range entries {
			key := r.Colorize(fmt.Sprintf("%-*s", keyWidth, e.key), ColorYellow)
			rows = append(rows, key+" "+e.desc)
		}
		return lipgloss.JoinVertical(lipgloss.Left, rows...)
	}

	// Each column padded right so the next column starts at a clean offset.
	padCol := lipgloss.NewStyle().PaddingRight(3)
	col1 := padCol.Render(renderColumn(i18n.T("agent.menu.col.execution"), execution))
	col2 := padCol.Render(renderColumn(i18n.T("agent.menu.col.edit_context"), editContext))
	col3 := renderColumn(i18n.T("agent.menu.col.view"), view)

	body := lipgloss.JoinHorizontal(lipgloss.Top, col1, col2, col3)

	fmt.Println()
	fmt.Println(r.Colorize(i18n.T("agent.menu.header"), ColorLime+ColorBold))
	fmt.Println(r.Colorize(strings.Repeat("─", 60), ColorGray))
	fmt.Println(body)
	fmt.Println(r.Colorize(strings.Repeat("─", 60), ColorGray))
}

// RenderModeBanner draws the entry-banner used by /coder and /agent.
// Layout:
//
//	╭── 🛠  CODER MODE ─────────────────────────────╮
//	│  Objective  · <query>                         │
//	│  Workspace  · <wd>                            │
//	│  Policy     · read-only por padrão · …        │
//	╰───────────────────────────────────────────────╯
//
// Fields is a slice of (label, value) pairs so callers can pass any
// mode-specific metadata without growing the function signature. The
// label is dimmed (gray) and the value rendered in the default
// foreground so the eye lands on the value first.
func (r *UIRenderer) RenderModeBanner(icon, title string, color string, fields [][2]string) {
	// Compute the widest label so the values align in a column. Uses
	// runewidth via lipgloss.Width to handle emoji/CJK correctly.
	maxLabel := 0
	for _, f := range fields {
		if w := lipgloss.Width(f[0]); w > maxLabel {
			maxLabel = w
		}
	}

	rows := make([]string, 0, len(fields))
	for _, f := range fields {
		label := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(
			fmt.Sprintf("%-*s", maxLabel, f[0]),
		)
		rows = append(rows, label+r.Colorize("  ·  ", ColorGray)+f[1])
	}
	r.RenderTimelineEvent(icon, title, strings.Join(rows, "\n"), color)
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

// RenderTimelineEvent desenha um "card" estilizado com:
//   - cabeçalho `╭── icon title ─────╮` que se estende até a largura do conteúdo,
//   - bordas laterais `│  …  │` em cada linha,
//   - rodapé `╰──────────────────╯` do TAMANHO DO CONTEÚDO (não da tela).
//
// Antes, o rodapé ia até a borda direita do terminal, dando uma sensação de
// "vazamento" quando o conteúdo era pequeno. A nova versão calcula a largura
// alvo como o maior entre (a) maior linha de conteúdo + padding e
// (b) largura do header — limitada pelo terminal. Lipgloss faz o cálculo de
// largura visível corretamente (ANSI-aware) via lipgloss.Width.
func (r *UIRenderer) RenderTimelineEvent(icon, title, content, color string) {
	termWidth, _, err := term.GetSize(int(os.Stdout.Fd())) //#nosec G115 -- value bounded by domain
	if err != nil || termWidth <= 0 {
		termWidth = 80
	}

	// Quanto o card pode ocupar no máximo. Mantemos 2 colunas de respiro
	// na borda direita para que terminais com scrollbar nativo (iTerm,
	// VSCode) não cortem o rodapé do card.
	maxCardWidth := termWidth - 2
	if maxCardWidth < 24 {
		maxCardWidth = 24
	}

	// Largura útil de conteúdo: subtrai borda esquerda "│  " (3) e borda
	// direita " │" (2). Isso é o limite para wrap.
	const sideLeft = 3  // "│  "
	const sideRight = 2 // " │"
	contentWrap := maxCardWidth - sideLeft - sideRight
	if contentWrap < 20 {
		contentWrap = 20
	}

	// Glamour-rendered markdown frequently bookends the output with
	// blank lines (one above the first paragraph, one or two below
	// the last). Without trimming, the card showed a leading `│   │`
	// row right after the ╭── header and a stack of empty rows
	// before the ╰── footer — read as if the box were "broken open"
	// at the top and bottom. Strip the surrounding newlines BEFORE
	// wrap so the wrapped slice starts and ends on real content.
	content = strings.Trim(content, "\n\r")
	wrappedLines := wrapText(content, contentWrap)
	wrappedLines = trimBlankBorderRows(wrappedLines)

	// Maior largura visível entre todas as linhas wrapped + header.
	header := fmt.Sprintf("%s %s", icon, title)
	headerVisible := VisibleLen(header) + 4 // "╭── " prefix
	maxContent := 0
	for _, line := range wrappedLines {
		if w := VisibleLen(line); w > maxContent {
			maxContent = w
		}
	}
	innerWidth := maxContent
	headerInner := headerVisible - sideLeft
	if headerInner > innerWidth {
		innerWidth = headerInner
	}
	// Pelo menos 24 colunas internas para não ficar grudado.
	if innerWidth < 24 {
		innerWidth = 24
	}
	// Limita ao espaço útil real.
	if innerWidth > contentWrap {
		innerWidth = contentWrap
	}

	cardWidth := innerWidth + sideLeft + sideRight
	if cardWidth > maxCardWidth {
		cardWidth = maxCardWidth
	}

	fmt.Println()

	// Top: "╭── icon title " + traços + "╮"
	topUsed := 4 + VisibleLen(header) // "╭── " + header
	topPad := cardWidth - topUsed - 1 // -1 for the closing "╮"
	if topPad < 1 {
		topPad = 1
	}
	topLine := "╭── " + header + " " + strings.Repeat("─", topPad-1) + "╮"
	fmt.Println(r.Colorize(topLine, color+ColorBold))

	// Bordas laterais coloridas, conteúdo no foreground default.
	borderL := r.Colorize("│", color) + "  "
	borderR := " " + r.Colorize("│", color)
	for _, line := range wrappedLines {
		w := VisibleLen(line)
		pad := innerWidth - w
		if pad < 0 {
			pad = 0
		}
		fmt.Println(borderL + line + strings.Repeat(" ", pad) + borderR)
	}

	// Bottom: "╰" + traços + "╯"
	bottomInner := cardWidth - 2
	if bottomInner < 1 {
		bottomInner = 1
	}
	bottom := "╰" + strings.Repeat("─", bottomInner) + "╯"
	fmt.Println(r.Colorize(bottom, color))
}

// RenderMarkdownTimelineEvent renderiza markdown (já convertido para ANSI fora) dentro do card.
// Ele só delega para RenderTimelineEvent, mas existe para explicitar intenção e padronizar chamadas.
func (r *UIRenderer) RenderMarkdownTimelineEvent(icon, title, renderedMarkdownANSI, color string) {
	if strings.TrimSpace(renderedMarkdownANSI) == "" {
		return
	}
	r.RenderTimelineEvent(icon, title, renderedMarkdownANSI, color)
}

// trimBlankBorderRows drops fully-blank rows from the leading and
// trailing edges of a wrapped-text slice. A row is "blank" when it
// has zero visible width — color codes alone don't count as visible
// content (lipgloss/glamour emit ANSI-only lines for some markdown
// constructs, and we don't want those drawing as ghost rows inside
// the card). Blank rows in the MIDDLE are preserved so paragraph
// breaks the author put in markdown survive.
func trimBlankBorderRows(rows []string) []string {
	start := 0
	for start < len(rows) && VisibleLen(rows[start]) == 0 {
		start++
	}
	end := len(rows)
	for end > start && VisibleLen(rows[end-1]) == 0 {
		end--
	}
	if start == 0 && end == len(rows) {
		return rows
	}
	return rows[start:end]
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
	r.RenderTimelineEvent("🧠", i18n.T("agent.ui.reasoning_title"), thought, ColorCyan)
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

	content := fmt.Sprintf("%s: %s\n%s: %s",
		i18n.T("agent.ui.tool_label"),
		r.Colorize(toolName, ColorYellow+ColorBold),
		i18n.T("agent.ui.tool_args"),
		displayArgs)

	r.RenderTimelineEvent("🔨", i18n.T("agent.ui.action_title"), content, ColorYellow)
}

// RenderToolResult exibe o resultado da execução
func (r *UIRenderer) RenderToolResult(output string, isError bool) {
	icon := "✅"
	title := i18n.T("agent.ui.result_success")
	color := ColorGreen

	if isError {
		icon = "❌"
		title = i18n.T("agent.ui.result_failure")
		color = ColorRed
	}

	// Truncar output muito grande para não poluir a timeline visualmente
	// O agente recebe tudo, mas o humano vê um resumo se for gigante
	displayOutput := output
	if len(output) > 2000 {
		displayOutput = output[:2000] + i18n.T("agent.ui.result_truncated")
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

	title := i18n.T("agent.ui.action_title_short", current, total)
	content := fmt.Sprintf("%s %s",
		r.Colorize(toolName, ColorYellow+ColorBold),
		r.Colorize(displayArgs, ColorCyan))

	r.RenderTimelineEvent("⚙️", title, content, ColorYellow)
}

// RenderToolResultMinimal exibe o resultado em modo compacto
func (r *UIRenderer) RenderToolResultMinimal(output string, isError bool) {
	icon := "✅"
	title := i18n.T("agent.ui.result_ok")
	color := ColorGreen

	if isError {
		icon = "❌"
		title = i18n.T("agent.ui.result_error")
		color = ColorRed
	}

	display := strings.TrimSpace(output)
	if idx := strings.Index(display, "\n"); idx >= 0 {
		display = display[:idx]
	}
	if len(display) > 240 {
		display = display[:240] + "..."
	}
	if display == "" {
		display = i18n.T("agent.ui.result_empty")
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

	msg := i18n.T("agent.ui.batch_started", totalActions)
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

	content := fmt.Sprintf("%s: %s\n%s: %s",
		i18n.T("agent.ui.tool_label"),
		r.Colorize(toolName, ColorYellow+ColorBold),
		i18n.T("agent.ui.tool_command"),
		r.Colorize(displayArgs, ColorCyan))

	title := i18n.T("agent.ui.action_title_progress", current, total)

	r.RenderTimelineEvent("🔨", title, content, ColorYellow)
}

// RenderBatchSummary exibe o resultado final do lote
func (r *UIRenderer) RenderBatchSummary(successCount, total int, hasError bool) {
	fmt.Println()
	if hasError {
		msg := i18n.T("agent.ui.batch_interrupted", successCount, total)
		fmt.Println(r.Colorize(msg, ColorYellow))
	} else {
		msg := i18n.T("agent.ui.batch_completed", total)
		fmt.Println(r.Colorize(msg, ColorGreen))
	}
	fmt.Println(r.Colorize(strings.Repeat("─", 60), ColorGray))
}

// streamBoxHeaderWidth captures the visible width of the header drawn by
// RenderStreamBoxStart so RenderStreamBoxEnd can produce a footer of the
// same length instead of stretching to the terminal edge. It is package-
// level because the start/end pair runs on the same goroutine inside a
// tool-execution loop; concurrent streaming boxes are not supported in
// this renderer, so a sync.Mutex would be ceremonial overhead.
var streamBoxHeaderWidth int

// RenderStreamBoxStart draws the top edge of an open card whose contents
// will be streamed in via StreamOutput. The bottom edge is closed by
// RenderStreamBoxEnd. Header width is recorded so the footer matches it.
func (r *UIRenderer) RenderStreamBoxStart(icon, title, color string) {
	header := fmt.Sprintf("%s %s", icon, title)

	termWidth, _, err := term.GetSize(int(os.Stdout.Fd())) //#nosec G115 -- value bounded by domain
	if err != nil || termWidth <= 0 {
		termWidth = 80
	}

	// "╭── " (4) + header + " " + minimum 6 dashes for visual weight.
	const minTail = 6
	headerLine := "╭── " + header + " " + strings.Repeat("─", minTail)
	visible := VisibleLen(headerLine)
	// Extend to a reasonable middle width when the header is short, but
	// never wider than the terminal minus a 2-col right gutter.
	target := termWidth - 2
	if target < visible {
		target = visible
	}
	if extra := target - visible; extra > 0 {
		headerLine += strings.Repeat("─", extra)
	}
	streamBoxHeaderWidth = VisibleLen(headerLine)

	fmt.Println()
	fmt.Println(r.Colorize(headerLine, color+ColorBold))
}

// RenderStreamBoxEnd closes the streaming card. Footer length mirrors
// the header so the box reads as a balanced shape regardless of how
// many lines the stream produced.
func (r *UIRenderer) RenderStreamBoxEnd(color string) {
	footerLen := streamBoxHeaderWidth - 1
	if footerLen < 10 {
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
//
// Tool label in cyan (was gray) so the tool identity stands out from
// the surrounding plain-gray prose. Without this, a "↻ Read(main.go)"
// looked identical to a free-text note in the timeline.
func (r *UIRenderer) CompactToolStart(toolLabel string) {
	fmt.Printf("  %s %s\n",
		r.Colorize("↻", ColorCyan),
		r.Colorize(toolLabel, ColorCyan))
}

// CompactToolDone renders a completed tool call in compact format:
//
//	✓ Read(main.go) 1.2s
//
// Tool label in cyan to match CompactToolStart; the green check + cyan
// duration already mark this as a result so the label color reinforces
// the tool identity rather than competing with the success signal.
func (r *UIRenderer) CompactToolDone(toolLabel string, duration string, isError bool) {
	if isError {
		fmt.Printf("  %s %s %s\n",
			r.Colorize("✗", ColorRed+ColorBold),
			r.Colorize(toolLabel, ColorCyan),
			r.Colorize(duration, ColorRed))
	} else {
		fmt.Printf("  %s %s %s\n",
			r.Colorize("✓", ColorGreen+ColorBold),
			r.Colorize(toolLabel, ColorCyan),
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

// CompactAssistantText renders the assistant's free-form text in the
// compact timeline. Distinct from CompactLine: the text uses the
// terminal's default foreground (ColorReset) so the assistant's actual
// answer stands out from the surrounding gray tool prose. Without
// this, in coder-compact mode the answer was visually indistinguishable
// from "Read(main.go)" lines and tool result excerpts — all the same
// ColorGray weight. Multi-line answers are wrapped, preserving the
// timeline indentation and dropping empty lines at the edges.
func (r *UIRenderer) CompactAssistantText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	icon := r.Colorize("◆", ColorCyan)
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if i == 0 {
			fmt.Printf("  %s %s\n", icon, r.Colorize(trimmed, ColorReset))
			continue
		}
		// continuation: align under the first line's text column
		fmt.Printf("    %s\n", r.Colorize(trimmed, ColorReset))
	}
}

// EchoUserInput re-prints a line the user just submitted at the coder-
// mode interactive prompt, in green with a ❯ marker. The kernel echo
// during line-editing is uncolored, and once the line is committed it
// scrolls into history alongside gray tool lines and gray reasoning
// summaries — making it hard to tell at a glance where the user's
// instruction was. This persistent echo gives the user's directives a
// distinct visual lane.
func (r *UIRenderer) EchoUserInput(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	fmt.Printf("  %s %s\n",
		r.Colorize("❯", ColorGreen+ColorBold),
		r.Colorize(text, ColorGreen),
	)
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
		r.Colorize("✗", ColorRed+ColorBold),
		r.Colorize(msg, ColorRed))
}

// CompactBatchSummary renders a one-line batch summary.
//
//	✓ 4/4 ações concluídas
//	✗ 2/4 ações concluídas (com erros)
func (r *UIRenderer) CompactBatchSummary(successCount, total int, hasError bool) {
	if hasError {
		fmt.Printf("\n  %s %s\n",
			r.Colorize("✗", ColorRed),
			r.Colorize(i18n.T("agent.ui.batch_summary_errors", successCount, total), ColorRed))
	} else if total > 1 {
		fmt.Printf("\n  %s %s\n",
			r.Colorize("✓", ColorGreen+ColorBold),
			r.Colorize(i18n.T("agent.ui.batch_summary_success", successCount, total), ColorGreen))
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

// statusPhraseKeys is the i18n key list backing LocalizedStatusPhrases.
// Adding a new phrase requires both a new key here and the matching
// agent.thinking.phrase_<N> entry in every locale file.
var statusPhraseKeys = []string{
	"agent.thinking.phrase_1",
	"agent.thinking.phrase_2",
	"agent.thinking.phrase_3",
	"agent.thinking.phrase_4",
	"agent.thinking.phrase_5",
	"agent.thinking.phrase_6",
	"agent.thinking.phrase_7",
	"agent.thinking.phrase_8",
	"agent.thinking.phrase_9",
	"agent.thinking.phrase_10",
}

// LocalizedStatusPhrases returns the rotating "thinking" messages in the
// currently active locale. Resolved at call time (not at package init)
// because i18n.Init may not have run yet when var-level initializers
// fire — calling it eagerly would freeze the slice to the raw keys.
func LocalizedStatusPhrases() []string {
	out := make([]string, 0, len(statusPhraseKeys))
	for _, k := range statusPhraseKeys {
		out = append(out, i18n.T(k))
	}
	return out
}
