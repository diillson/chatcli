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
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
	"go.uber.org/zap"
)

// typewriterPrint emits text rune-by-rune with a small delay between
// printable runes so the line "types" onto the screen. ANSI escape
// sequences are flushed instantly so colors/styles never pause the eye.
func typewriterPrint(text string, delay time.Duration) {
	inEsc := false
	for _, ch := range text {
		if ch == '\033' {
			inEsc = true
		}
		fmt.Printf("%c", ch)
		_ = os.Stdout.Sync()
		if inEsc {
			if ch == 'm' {
				inEsc = false
			}
			continue
		}
		time.Sleep(delay)
	}
}

// ANSI Color codes exportados
//
// Legacy path: hue constants (duplicated from cli/colors.go — the
// duplication is exactly what ui/kit exists to end). They remain because
// they are exported and load-bearing across cli, but new and migrated code
// styles through kit.Colorize / kit.Style with a theme.Role.
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

	// Descobre a largura útil do box para quebrar linhas muito longas
	// (ex.: `kubectl ... -o yaml` com annotations gigantes / blocos
	// `last-applied-configuration`). Sem isso, uma linha que estoura a
	// largura do terminal faz reflow e rasga a borda lateral/rodapé do box.
	termWidth := kit.TermWidth()

	// Cada linha emitida é: prefix("│  ") + icon + conteúdo. A largura
	// visível total precisa caber em termWidth-2 (mesma gutter de 2 cols
	// que o resto do renderer reserva pra scrollbar nativa de terminais).
	emit := func(icon, text, color string) {
		avail := termWidth - 2 - VisibleLen("│  ") - VisibleLen(icon)
		if avail < 20 {
			avail = 20
		}
		for _, seg := range wrapStreamLine(text, avail) {
			fmt.Println(prefix + r.Colorize(icon+seg, color))
		}
	}

	// O callback normalmente entrega uma linha por vez, mas defendemos
	// contra blocos com \n embutido para nunca emitir uma linha sem prefixo.
	for _, sub := range strings.Split(line, "\n") {
		if strings.HasPrefix(sub, "ERR: ") {
			// kit.GlyphWarn é 1 célula fixa (sem VS-16), então o cálculo de
			// largura nunca deriva — o "⚠️ " antigo media 1 e pintava 2.
			emit(kit.GlyphWarn.String()+"  ", strings.TrimPrefix(sub, "ERR: "), ColorYellow)
		} else {
			emit("  ", sub, ColorGray) // indentação padrão
		}
	}
}

// wrapStreamLine quebra UMA linha de output cru de tool na largura visível
// informada, preservando indentação. Matemática em kit.WrapStreamLine.
func wrapStreamLine(line string, width int) []string {
	return kit.WrapStreamLine(line, width)
}

// Colorize aplica cores ANSI (exportada com C maiúsculo). O resultado passa
// por theme.Recolor para que os códigos básicos legados (ColorCyan, ColorYellow,
// …) adotem a hue do tema ativo sob o profile de cor atual — toda a UI
// re-tematiza sem alterar os call sites, e a saída fica limpa (sem ANSI) quando
// não há terminal colorido (pipe/CI).
func (r *UIRenderer) Colorize(text string, color string) string {
	return theme.Recolor(fmt.Sprintf("%s%s%s", color, text, ColorReset))
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
		status := kit.Sym(kit.GlyphRunning)
		if i < len(outputs) && outputs[i] != nil {
			if strings.TrimSpace(outputs[i].ErrorMsg) == "" {
				status = kit.Sym(kit.GlyphSuccess)
			} else {
				status = kit.Sym(kit.GlyphError)
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
	maxLines := 30
	if len(lines) > maxLines {
		preview := strings.Join(lines[:maxLines], "\n") + "\n...\n"
		fmt.Print(preview)
	} else {
		fmt.Println(out)
	}

	tipsMessage := i18n.T("agent.last_result.tips", lastIdx+1, lastIdx+1)
	fmt.Printf("\n%s\n", tipsMessage)
}

// PrintHeader imprime o cabeçalho do modo agente: uma régua titulada
// responsiva (hierarquia título-bold + traços dim) no lugar das duas linhas
// de ━ com 58 colunas fixas.
func (r *UIRenderer) PrintHeader() {
	fmt.Println()
	fmt.Println(kit.RuleTitled(i18n.T("agent.header.title")))
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
		head := kit.Style(theme.RoleHeader).Bold(true).Render(title)
		sep := kit.Style(theme.RoleMuted).Render(strings.Repeat("─", lipgloss.Width(head)))
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
	fmt.Println(kit.RuleTitled(i18n.T("agent.menu.header")))
	fmt.Println(body)
	fmt.Println(kit.Rule())
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
		// kit.PadRight mede largura visível — "%-*s" contava bytes e
		// desalinhava labels traduzidos acentuados neste banner.
		label := kit.Style(theme.RoleMuted).Render(kit.PadRight(f[0], maxLabel))
		rows = append(rows, label+r.Colorize("  ·  ", ColorGray)+f[1])
	}
	r.RenderTimelineEvent(icon, title, strings.Join(rows, "\n"), color)
}

// PrintPrompt imprime o prompt de entrada
func (r *UIRenderer) PrintPrompt() string {
	return r.Colorize(i18n.T("agent.prompt.choice"), ColorLime)
}

// VisibleLen calcula comprimento visível em colunas do terminal (sem ANSI
// codes). Delegates to kit.VisibleLen (lipgloss.Width) — one measurement
// path for wrap math and border math keeps them in agreement when content
// has emoji presentation sequences.
func VisibleLen(s string) int {
	return kit.VisibleLen(s)
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
	r.renderTimelineEventInner(icon, title, content, color, false, wrapText)
}

// RenderAssistantResponseTimelineEvent draws the same card as
// RenderTimelineEvent but types the body progressively so the final
// assistant message feels alive instead of a single paste. Reserved for
// the model's "RESPOSTA/RESUMO" card — tool calls and reasoning still use
// the instant path because typing those would slow the agent loop down.
//
// The body is glamour-rendered markdown, so it wraps with wrapStructured
// (not wrapText): replies that embed YAML/JSON/code keep their indentation
// instead of collapsing flush-left.
func (r *UIRenderer) RenderAssistantResponseTimelineEvent(icon, title, content, color string) {
	r.renderTimelineEventInner(icon, title, content, color, true, wrapStructured)
}

// renderTimelineEventInner draws a titled card. wrapFn owns how the body is
// fit to the inner width: wrapText for prose (reasoning / responses, where
// collapsing whitespace into word-wrap reads best) and wrapPreserve for raw
// tool output (YAML/JSON/tables, where indentation and column alignment must
// survive).
func (r *UIRenderer) renderTimelineEventInner(icon, title, content, color string, typewrite bool, wrapFn func(string, int) []string) {
	// kit.ContentWidth already reserves the right-edge gutter for native
	// scrollbars (iTerm, VSCode) so the box border never gets clipped.
	maxCardWidth := kit.ContentWidth()
	if maxCardWidth < 24 {
		maxCardWidth = 24
	}

	// Trim glamour-style bookend newlines so the card opens on real
	// content. See trimBlankBorderRows() for the in-body equivalent.
	content = strings.Trim(content, "\n\r")
	if content == "" {
		content = " " // lipgloss collapses fully-empty content; keep a placeholder so the box still draws
	}

	// Pre-wrap content to the inner width lipgloss will allow. lipgloss
	// would happily truncate on overflow rather than wrap, so we own
	// the wrap math here using the same ANSI-aware helper the previous
	// renderer used. Inner = card max − borders (2) − padding (4).
	const innerOverhead = 2 /* borders */ + 4 /* Padding(0,2) */
	innerWrap := maxCardWidth - innerOverhead
	if innerWrap < 20 {
		innerWrap = 20
	}
	wrapped := strings.Join(wrapFn(content, innerWrap), "\n")

	// Build the body box with lipgloss using a CLOSED border on three
	// sides (no top — we overwrite it below with the titled variant).
	// Delegating width math + side+bottom drawing to lipgloss is what
	// fixes the long-standing emoji misalignment bug (`🧠` rendering
	// as 2 cols via runewidth but 1 col on some terminals): both edges
	// agree with each other regardless of the terminal's actual emoji
	// handling, so the box always reads as balanced.
	bodyStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderTop(false).
		BorderForeground(ansiColorToLip(color)).
		Padding(0, 2)

	bodyRendered := bodyStyle.Render(wrapped)

	// Drop empty rows from the in-body sequence so paragraph-style
	// blanks at the edges don't show as ghost rows. We keep middle
	// blanks intact (author-intended paragraph breaks survive).
	bodyRendered = trimBlankBoxBodyRows(bodyRendered)

	// cardWidth is whatever lipgloss measured for the rendered body.
	// Crucially, ANY width drift in the top header below is computed
	// against this same number, so the visible widths line up even
	// when emoji rendering disagrees with runewidth.
	cardWidth := lipgloss.Width(bodyRendered)

	header := fmt.Sprintf("%s %s", icon, title)
	topLine := buildTitledTopBorder(header, cardWidth, color, r)

	fmt.Println()
	fmt.Println(topLine)
	if typewrite {
		PaceText(bodyRendered, defaultDelay)
		fmt.Println()
	} else {
		fmt.Println(bodyRendered)
	}
}

// buildTitledTopBorder produces a `╭── icon title ─────╮` line whose
// VISIBLE width equals targetWidth (as measured by lipgloss.Width on
// the matching body). The two padding rules cover the two ways the
// header can fall short of the card width:
//   - normal case: title fits, fill with dashes
//   - title longer than card: truncate dashes to fit (header may overflow
//     by 1-2 cols on extreme widths; acceptable degradation)
func buildTitledTopBorder(header string, targetWidth int, color string, r *UIRenderer) string {
	// Geometry in kit.TitledTopBorder; coloring stays here so the legacy
	// ANSI-constant call sites keep byte-identical output.
	return r.Colorize(kit.TitledTopBorder(header, targetWidth), color+ColorBold)
}

// trimBlankBoxBodyRows removes empty body rows adjacent to the box borders.
// Logic in kit.TrimBlankBoxBodyRows.
func trimBlankBoxBodyRows(rendered string) string {
	return kit.TrimBlankBoxBodyRows(rendered)
}

// stripANSIForCard removes CSI color escapes so width and emptiness checks
// see plain text. Logic in kit.StripANSI.
func stripANSIForCard(s string) string {
	return kit.StripANSI(s)
}

// ansiColorToLip maps the package-local ANSI color constants used by
// the rest of the renderer into a lipgloss.Color. Callers keep passing
// the same "\x1b[36m" strings they always have; resolution is delegated
// to the active theme (theme.LipFromANSI), so a theme swap re-skins every
// card border without touching a single call site. Unknown values fall
// back to the terminal default so a typo doesn't make the border disappear.
func ansiColorToLip(ansiCode string) lipgloss.Color {
	return theme.LipFromANSI(ansiCode)
}

// RenderMarkdownTimelineEvent renderiza markdown (já convertido para ANSI fora) dentro do card.
// Usa wrapStructured (não o wrapText de RenderTimelineEvent) para que YAML/JSON/código embutidos
// no markdown preservem a indentação dentro do card — o mesmo fix aplicado ao envelope de chat.
func (r *UIRenderer) RenderMarkdownTimelineEvent(icon, title, renderedMarkdownANSI, color string) {
	if strings.TrimSpace(renderedMarkdownANSI) == "" {
		return
	}
	r.renderTimelineEventInner(icon, title, renderedMarkdownANSI, color, false, wrapStructured)
}

// trimBlankBorderRows drops fully-blank edge rows from a wrapped-text
// slice, keeping middle blanks. Logic in kit.TrimBlankBorderRows.
func trimBlankBorderRows(rows []string) []string {
	return kit.TrimBlankBorderRows(rows)
}

// wrapText quebra o texto em linhas que não excedem o limite (word-wrap de
// prosa). A matemática vive em ui/kit (kit.WrapText); este delegate preserva
// os call sites e testes históricos do pacote.
func wrapText(text string, limit int) []string {
	return kit.WrapText(text, limit)
}

// wrapPreserve quebra texto preservando estrutura/indentação (tool output).
// Matemática em kit.WrapPreserve.
func wrapPreserve(text string, limit int) []string {
	return kit.WrapPreserve(text, limit)
}

// wrapStructured quebra corpos renderizados pelo glamour preservando (e
// dedentando) a indentação. Matemática em kit.WrapStructured.
func wrapStructured(text string, limit int) []string {
	return kit.WrapStructured(text, limit)
}

// splitLeadingIndent separa indentação inicial de conteúdo, ANSI-aware.
// Lógica em kit.SplitLeadingIndent.
func splitLeadingIndent(line string) (indent int, codes string, content string) {
	return kit.SplitLeadingIndent(line)
}

// hardBreakWord parte uma palavra maior que o limite em pedaços rune-aware.
// Lógica em kit.HardBreakWord.
func hardBreakWord(w string, limit int) []string {
	return kit.HardBreakWord(w, limit)
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

// RenderToolResult exibe o resultado da execução. O glifo do título vem do
// vocabulário canônico (✓/✗, 1 célula, sem propriedade Emoji) e herda a cor
// da borda do card — verde/vermelho continuam carregando o estado.
func (r *UIRenderer) RenderToolResult(output string, isError bool) {
	icon := kit.GlyphSuccess.String()
	title := i18n.T("agent.ui.result_success")
	color := ColorGreen

	if isError {
		icon = kit.GlyphError.String()
		title = i18n.T("agent.ui.result_failure")
		color = ColorRed
	}

	// Truncar output muito grande para não poluir a timeline visualmente
	// O agente recebe tudo, mas o humano vê um resumo se for gigante
	displayOutput := output
	if len(output) > 2000 {
		displayOutput = output[:2000] + i18n.T("agent.ui.result_truncated")
	}

	// Tool output cru (YAML/JSON/tabelas) usa o wrap que preserva
	// indentação e alinhamento de colunas — o mesmo da streaming box —
	// em vez do word-wrap de prosa que colapsaria a estrutura.
	r.renderTimelineEventInner(icon, title, displayOutput, color, false, wrapPreserve)
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
	icon := kit.GlyphSuccess.String()
	title := i18n.T("agent.ui.result_ok")
	color := ColorGreen

	if isError {
		icon = kit.GlyphError.String()
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

// RenderBatchHeader exibe um cabeçalho indicando o início de um lote —
// uma régua titulada responsiva no lugar do sanduíche de ═ em tela cheia.
func (r *UIRenderer) RenderBatchHeader(totalActions int) {
	fmt.Println()
	fmt.Println(kit.RuleTitled(i18n.T("agent.ui.batch_started", totalActions)))
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
	fmt.Println(kit.Rule())
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

	termWidth := kit.TermWidth()

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
	icon := r.Colorize(kit.GlyphAssistant.String(), ColorCyan)
	for i, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		var prefix string
		if i == 0 {
			prefix = "  " + icon + " "
		} else {
			prefix = "    "
		}
		fmt.Print(prefix)
		PaceText(r.Colorize(trimmed, ColorReset), defaultDelay)
		fmt.Println()
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

// StatusPhrases is the original English fallback list, retained for
// backward-compat with code outside this package that imported the
// package-level slice. New code should call LocalizedStatusPhrases()
// so locale changes are honored at call time.
//
// Deprecated: use LocalizedStatusPhrases().
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
