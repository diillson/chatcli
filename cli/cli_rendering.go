package cli

import (
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/ui/theme"
)

// chipSentinel is the marker injected as a standalone paragraph above each
// top-level fenced code block before rendering, then swapped for the styled
// language chip after rendering. The guillemets are not markdown-active and
// are vanishingly unlikely to occur in real content, so glamour passes the
// marker through verbatim as a single text run (one color span), which makes
// it reliable to find and replace afterwards.
const (
	chipSentinelOpen  = "«clichip:"
	chipSentinelClose = "»"
)

// renderMarkdown renders Markdown to ANSI using the ACTIVE theme. Colors for
// headings, links, inline code and fenced code blocks (chroma syntax tokens)
// all derive from the same palette as the cards and borders, so a theme swap
// re-skins markdown in lock-step.
//
// The WHOLE document is rendered in a single glamour call so document-scoped
// features resolve correctly: reference-style links and footnotes (whose
// definitions may sit far from their use), ordered-list numbering, and the
// blank-line spacing glamour puts around block elements. Language chips for
// top-level code blocks are added by injecting a sentinel paragraph before
// each block and replacing it with the styled "▌ lang" line post-render — so
// the chip never costs us the correctness of whole-document rendering.
func (cli *ChatCLI) renderMarkdown(input string) string {
	prof := theme.ActiveProfile()
	renderer, err := glamour.NewTermRenderer(theme.Active().GlamourOptions(prof)...)
	if err != nil {
		return input
	}

	src, hasChips := injectChipSentinels(input)
	out, rerr := renderer.Render(src)
	if rerr != nil {
		return input
	}
	if hasChips {
		out = replaceChipSentinels(out, prof)
	}

	out = strings.TrimRight(out, " \n\t")
	if !strings.HasSuffix(out, "\033[0m") {
		out += "\033[0m"
	}
	return out
}

// injectChipSentinels rewrites the source so each top-level fenced code block
// with a declared language is preceded by a sentinel paragraph. Returns the
// rewritten source and whether any sentinel was added. Reconstructing the
// document from the segments is loss-less: splitTopLevelCodeBlocks partitions
// the lines contiguously, so joining the parts with "\n" reproduces the
// original text (plus the inserted sentinels).
func injectChipSentinels(input string) (string, bool) {
	segs := splitTopLevelCodeBlocks(input)
	has := false
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		if s.isCode && s.lang != "" {
			// Blank line + sentinel line, so glamour treats the sentinel as a
			// standalone paragraph immediately above the code block.
			parts = append(parts, "\n"+chipSentinelOpen+s.lang+chipSentinelClose+"\n"+s.text)
			has = true
		} else {
			parts = append(parts, s.text)
		}
	}
	return strings.Join(parts, "\n"), has
}

// replaceChipSentinels swaps each rendered sentinel line for the themed
// language chip. It matches on the raw line (the marker survives as one
// contiguous text run) and replaces the entire line, discarding glamour's
// own coloring of the marker.
func replaceChipSentinels(rendered string, prof theme.Profile) string {
	lines := strings.Split(rendered, "\n")
	for i, ln := range lines {
		open := strings.Index(ln, chipSentinelOpen)
		if open < 0 {
			continue
		}
		rest := ln[open+len(chipSentinelOpen):]
		close := strings.Index(rest, chipSentinelClose)
		if close < 0 {
			continue
		}
		lang := rest[:close]
		lines[i] = codeLanguageChip(lang, prof)
	}
	return strings.Join(lines, "\n")
}

// mdSegment is one piece of a markdown document: either a top-level fenced
// code block (isCode, with its language) or a run of everything else.
type mdSegment struct {
	text   string
	isCode bool
	lang   string
}

// splitTopLevelCodeBlocks scans the input line by line and splits it into
// alternating prose and TOP-LEVEL fenced code segments. A fence is top-level
// only when its ``` (or ~~~) opener starts at column 0 — indented fences
// (e.g. inside a list item) stay within their prose segment so glamour keeps
// the surrounding structure intact. A stateful line scan is used rather than
// a regex because fence info strings and nested back-ticks make regex
// matching brittle (a lesson the codebase already encodes elsewhere).
func splitTopLevelCodeBlocks(input string) []mdSegment {
	lines := strings.Split(input, "\n")
	var segs []mdSegment
	var prose []string

	flushProse := func() {
		if len(prose) > 0 {
			segs = append(segs, mdSegment{text: strings.Join(prose, "\n")})
			prose = prose[:0]
		}
	}

	for i := 0; i < len(lines); i++ {
		fence, lang, ok := topLevelFenceOpener(lines[i])
		if !ok {
			prose = append(prose, lines[i])
			continue
		}
		// Found an opener at column 0; collect through the matching closer.
		block := []string{lines[i]}
		j := i + 1
		closed := false
		for ; j < len(lines); j++ {
			block = append(block, lines[j])
			if isFenceCloser(lines[j], fence) {
				closed = true
				break
			}
		}
		if !closed {
			// Unterminated fence — treat the rest as prose so we never drop
			// content. Glamour will still render it sensibly.
			prose = append(prose, lines[i:]...)
			break
		}
		flushProse()
		segs = append(segs, mdSegment{text: strings.Join(block, "\n"), isCode: true, lang: lang})
		i = j
	}
	flushProse()
	return segs
}

// topLevelFenceOpener reports whether a line opens a fenced code block at
// column 0, returning the fence token ("```" or "~~~") and the declared
// language (the info string's first word, lowercased).
func topLevelFenceOpener(line string) (fence, lang string, ok bool) {
	switch {
	case strings.HasPrefix(line, "```"):
		fence = "```"
	case strings.HasPrefix(line, "~~~"):
		fence = "~~~"
	default:
		return "", "", false
	}
	info := strings.TrimSpace(strings.TrimPrefix(line, fence))
	// A line that is only the fence with trailing back-ticks is not an opener
	// with a language; info may be empty (plain code block).
	if idx := strings.IndexAny(info, " \t"); idx >= 0 {
		info = info[:idx]
	}
	return fence, strings.ToLower(info), true
}

// isFenceCloser reports whether a line closes a fence opened with the given
// token. A closer is the fence token at column 0 with only optional trailing
// whitespace after it.
func isFenceCloser(line, fence string) bool {
	if !strings.HasPrefix(line, fence) {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(line, fence)) == ""
}

// codeLanguageChip builds the themed language badge shown above a code block,
// indented to align with glamour's 2-column code-block margin. The bar uses
// the accent color and the label the muted color; on non-color profiles both
// SGR helpers return "" so the chip degrades to plain "  ▌ yaml".
func codeLanguageChip(lang string, prof theme.Profile) string {
	bar := theme.Active().ColorFor(theme.RoleHeader).SGR(prof)
	label := theme.Active().ColorFor(theme.RoleMuted).SGR(prof)
	reset := theme.Reset()
	return "  " + bar + "▌ " + reset + label + lang + reset
}

// ensureANSIReset garante que string termina com reset ANSI
func ensureANSIReset(s string) string {
	if !strings.HasSuffix(s, "\033[0m") && !strings.HasSuffix(s, "\033[m") {
		return s + "\033[0m"
	}
	return s
}

// typewriterEffect exibe o texto com efeito de máquina de escrever
// usando o pacing adaptativo do pacote agent: respostas curtas mantêm
// a cadência solicitada (delay por rune), respostas longas têm o delay
// escalonado para caber no orçamento total (~800ms por padrão), e
// respostas muito grandes (acima de 8k runas visíveis) são pintadas
// instantaneamente. Variáveis de ambiente CHATCLI_NO_TYPEWRITER,
// CHATCLI_TYPEWRITER_BUDGET_MS e CHATCLI_TYPEWRITER_DELAY_MS permitem
// ajuste fino sem rebuild.
//
// Mantemos esta função como método do ChatCLI por compatibilidade com
// os call sites históricos; a lógica real vive em agent.PaceText e é
// compartilhada com o envelope de resposta.
func (cli *ChatCLI) typewriterEffect(text string, delay time.Duration) {
	agent.PaceText(text, delay)
}
