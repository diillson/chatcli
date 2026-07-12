/*
 * ChatCLI - UI kit: ANSI-aware wrapping
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The three wrap flavors every boxed surface uses, moved verbatim from
 * cli/agent so chat, agent and command surfaces share one wrap math:
 *
 *   - WrapText: prose word-wrap (collapses whitespace) — reasoning, replies.
 *   - WrapStructured: glamour-rendered bodies — preserves (and dedents)
 *     indentation so YAML/JSON/code keeps its shape.
 *   - WrapPreserve / WrapStreamLine: raw tool output — every fitting line
 *     verbatim, only overflowing lines break, indentation repeated.
 */
package kit

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// WrapText quebra o texto em linhas que não excedem o limite.
// - Preserva quebras de linha originais
// - Faz word-wrap por largura visível (ignora ANSI)
// - Não destrói formatação do markdown renderizado (ANSI + linhas)
func WrapText(text string, limit int) []string {
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

		// emitLongWord quebra uma palavra maior que o limite em pedaços
		// (rune-aware), empurra os pedaços completos para finalLines e
		// deixa o último pedaço como início da linha corrente.
		emitLongWord := func(w string) {
			chunks := HardBreakWord(w, limit)
			for i := 0; i < len(chunks)-1; i++ {
				finalLines = append(finalLines, chunks[i])
			}
			last := chunks[len(chunks)-1]
			line.WriteString(last)
			curLen = VisibleLen(last)
		}

		for _, w := range words {
			wLen := VisibleLen(w)
			if curLen == 0 {
				// Palavra única maior que o limite (ex.: o JSON de
				// `last-applied-configuration` sem espaços) precisa ser
				// quebrada aqui também — caso contrário ela é escrita
				// inteira e estoura a largura do box.
				if wLen > limit {
					emitLongWord(w)
				} else {
					line.WriteString(w)
					curLen = wLen
				}
				continue
			}

			// +1 espaço
			if curLen+1+wLen <= limit {
				line.WriteByte(' ')
				line.WriteString(w)
				curLen += 1 + wLen
				continue
			}

			// Não cabe na linha atual: fecha e recoloca a palavra,
			// quebrando "na marra" se ela sozinha já estoura o limite.
			flushLine()

			if wLen <= limit {
				line.WriteString(w)
				curLen = wLen
				continue
			}

			emitLongWord(w)
		}

		if line.Len() > 0 {
			finalLines = append(finalLines, line.String())
		}
	}

	return finalLines
}

// HardBreakWord parte uma palavra (sem espaços) em pedaços cuja largura
// visível não excede limit. A quebra é por runa, medindo com lipgloss.Width,
// para não cortar sequências UTF-8 / wide-runes no meio. Retorna ao menos
// um elemento.
func HardBreakWord(w string, limit int) []string {
	if limit <= 0 {
		return []string{w}
	}

	var out []string
	var cur strings.Builder
	curW := 0
	for _, rr := range w {
		rw := lipgloss.Width(string(rr))
		if rw < 1 {
			rw = 1
		}
		if curW+rw > limit && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
			curW = 0
		}
		cur.WriteRune(rr)
		curW += rw
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}

// WrapStreamLine quebra UMA linha de output cru de tool na largura visível
// informada. Diferente de WrapText, NÃO colapsa espaços em branco: a
// indentação inicial é preservada (e repetida nas continuações) para que
// YAML/JSON estruturado continue legível dentro do box. A quebra é por
// runa (ANSI/wide-rune aware via lipgloss.Width), evitando cortar
// sequências multibyte no meio.
func WrapStreamLine(line string, width int) []string {
	if width <= 0 || VisibleLen(line) <= width {
		return []string{line}
	}

	trimmed := strings.TrimLeft(line, " \t")
	indent := line[:len(line)-len(trimmed)]
	indentW := VisibleLen(indent)
	if indentW >= width-1 {
		// Indentação maior que o box: desiste de preservá-la.
		indent = ""
		indentW = 0
	}
	chunkW := width - indentW
	if chunkW < 1 {
		chunkW = 1
	}

	var out []string
	var cur strings.Builder
	curW := 0
	flush := func() {
		out = append(out, indent+cur.String())
		cur.Reset()
		curW = 0
	}

	for _, rr := range trimmed {
		rw := lipgloss.Width(string(rr))
		if rw < 1 {
			rw = 1
		}
		if curW+rw > chunkW && cur.Len() > 0 {
			flush()
		}
		cur.WriteRune(rr)
		curW += rw
	}
	if cur.Len() > 0 {
		flush()
	}
	if len(out) == 0 {
		out = append(out, indent)
	}
	return out
}

// WrapPreserve quebra texto preservando a estrutura: cada linha que cabe no
// limite é mantida exatamente como está (indentação e espaçamento de colunas
// intactos) e só as que estouram são quebradas, repetindo a indentação nas
// continuações — usada em output cru de tool (YAML/JSON/tabelas), onde
// colapsar whitespace como o word-wrap de prosa destruiria o layout.
func WrapPreserve(text string, limit int) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		out = append(out, WrapStreamLine(line, limit)...)
	}
	return out
}

// SplitLeadingIndent separa a indentação inicial de uma linha do seu
// conteúdo, de forma ANSI-aware. O glamour emite sequências de cor de
// largura-zero ANTES (e entre) os espaços de indentação do markdown
// renderizado — então um strings.TrimLeft(" \t") reportaria indentação
// zero em YAML/JSON/código vindos do glamour. Retorna a largura visível do
// indent em colunas, os códigos ANSI vistos na região inicial (re-anexados
// ao conteúdo para que o primeiro token preserve a cor) e o conteúdo
// restante.
func SplitLeadingIndent(line string) (indent int, codes string, content string) {
	var cb strings.Builder
	i := 0
	for i < len(line) {
		// Sequência CSI ANSI: preserva — pode colorir o conteúdo à frente.
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j < len(line) {
				j++ // inclui o byte final da sequência
			}
			cb.WriteString(line[i:j])
			i = j
			continue
		}
		if line[i] == ' ' || line[i] == '\t' {
			indent++ // tab contado como 1 col (o glamour normaliza para espaços)
			i++
			continue
		}
		break
	}
	return indent, cb.String(), line[i:]
}

// WrapStructured quebra um corpo já renderizado pelo glamour para exibição
// dentro de um box. Diferente de WrapText (word-wrap de prosa, que colapsa
// a indentação via strings.Fields), ele PRESERVA a indentação inicial de
// cada linha. Mecânica, por linha:
//
//   - Detecta o indent inicial visível (ANSI-aware: o glamour emite cores
//     ANTES dos espaços, então um TrimLeft simples não enxerga o indent).
//   - Deslova (dedent) toda linha pela margem mínima comum — o glamour aplica
//     uma margem de documento uniforme (tipicamente 2 cols) a prosa E código;
//     como o box já tem seu próprio padding, carregar a margem do glamour
//     também empurraria tudo para a direita.
//   - Linhas que cabem na largura interna são emitidas verbatim (alinhamento
//     de colunas intacto). Só as que estouram passam por word-wrap, repetindo
//     o indent da linha em cada continuação.
func WrapStructured(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	rawLines := strings.Split(text, "\n")

	type lineSeg struct {
		indent  int
		payload string // códigos ANSI iniciais + conteúdo
		blank   bool
	}
	segs := make([]lineSeg, 0, len(rawLines))
	commonIndent := -1
	for _, ln := range rawLines {
		ind, codes, body := SplitLeadingIndent(ln)
		blank := strings.TrimSpace(StripANSI(ln)) == ""
		segs = append(segs, lineSeg{indent: ind, payload: codes + body, blank: blank})
		if !blank && (commonIndent < 0 || ind < commonIndent) {
			commonIndent = ind
		}
	}
	if commonIndent < 0 {
		commonIndent = 0
	}

	out := make([]string, 0, len(rawLines))
	for _, s := range segs {
		if s.blank {
			out = append(out, "")
			continue
		}
		rel := s.indent - commonIndent
		if rel < 0 {
			rel = 0
		}
		pad := strings.Repeat(" ", rel)
		full := pad + s.payload
		if VisibleLen(full) <= limit {
			// Cabe: verbatim — preserva qualquer alinhamento interno.
			out = append(out, full)
			continue
		}
		// Estoura: word-wrap do conteúdo, repetindo o indent nas continuações.
		avail := limit - rel
		if avail < 1 {
			avail = 1
		}
		for _, chunk := range WrapText(s.payload, avail) {
			out = append(out, pad+chunk)
		}
	}
	return out
}

// StripANSI removes CSI color escapes so width and emptiness checks see
// plain text. Loop-based to avoid a regex dependency on hot render paths.
func StripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// TrimBlankBorderRows drops fully-blank rows from the leading and trailing
// edges of a wrapped-text slice. A row is "blank" when it has zero visible
// width — color codes alone don't count as visible content. Blank rows in
// the MIDDLE are preserved so paragraph breaks the author put in markdown
// survive.
func TrimBlankBorderRows(rows []string) []string {
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

// TrimBlankBoxBodyRows removes fully-empty content rows directly adjacent
// to the top or bottom border of a lipgloss-rendered box. An empty row
// looks like "│         │" — same width as the sides but zero printable
// content between them. Middle blanks are kept (author-intended paragraph
// breaks survive).
func TrimBlankBoxBodyRows(rendered string) string {
	rows := strings.Split(rendered, "\n")
	if len(rows) <= 2 {
		return rendered
	}
	isBlankRow := func(s string) bool {
		plain := StripANSI(s)
		// Empty body rows are "│  ...spaces...  │". Anything else counts
		// as real content (including a ╰── bottom border).
		if !strings.HasPrefix(plain, "│") || !strings.HasSuffix(plain, "│") {
			return false
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(plain, "│"), "│")
		return strings.TrimSpace(inner) == ""
	}
	// We do NOT touch the first row (it could be a border) or the last
	// (always the bottom border). Trim only the body slice in between.
	start := 0
	end := len(rows)
	for start < end && rows[start] != "" && !isBlankRow(rows[start]) {
		// Non-blank, real row — leave start where it is.
		break
	}
	// Trim trailing empty body rows that sit right before the bottom border.
	bottomIdx := end - 1
	cut := bottomIdx - 1
	for cut > start && isBlankRow(rows[cut]) {
		cut--
	}
	if cut+1 < bottomIdx {
		rows = append(rows[:cut+1], rows[bottomIdx])
	}
	// Trim leading empty body rows that sit right after the (absent) top
	// border. With BorderTop disabled, rows[0] is the first content row.
	leading := 0
	for leading < len(rows)-1 && isBlankRow(rows[leading]) {
		leading++
	}
	if leading > 0 {
		rows = rows[leading:]
	}
	return strings.Join(rows, "\n")
}
