package cli

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// ─── Terminal Box Helpers ──────────────────────────────────────
// Shared UI functions for rendering consistent boxed output
// across /mcp, /hooks, /cost, /channel, /worktree, etc.

func uiTermWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd())) //#nosec G115 -- value bounded by domain
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// uiBox renders a box header: ╭── icon TITLE
func uiBox(icon, title, color string) string {
	return fmt.Sprintf("%s%s╭── %s %s%s", color, ColorBold, icon, title, ColorReset)
}

// uiBoxEnd renders a box footer: ╰─────────
func uiBoxEnd(color string) string {
	return fmt.Sprintf("%s╰%s%s", color, strings.Repeat("─", uiTermWidth()-2), ColorReset)
}

// uiPrefix renders a box sidebar: │
func uiPrefix(color string) string {
	return fmt.Sprintf("%s│%s  ", color, ColorReset)
}

// uiBoxLine renders one content line inside a box, clamped to the terminal
// width. Lines wider than the terminal wrap, which breaks the box frame and
// desynchronizes go-prompt's row accounting on the next redraw — the box
// then appears overwritten or half-hidden. ANSI color sequences are
// preserved and never count toward the visible width.
func uiBoxLine(color, text string) string {
	const prefixCols = 3 // "│  " rendered by uiPrefix
	maxCols := uiTermWidth() - prefixCols - 1
	if maxCols < 8 {
		maxCols = 8
	}
	return uiPrefix(color) + ansiTruncate(text, maxCols)
}

// ansiTruncate clamps s to at most maxCols visible columns (wide runes count
// per display cell), preserving ANSI color sequences and appending an
// ellipsis plus a color reset when content was dropped so styling never
// bleeds into the next line.
func ansiTruncate(s string, maxCols int) string {
	if visibleLen(s) <= maxCols {
		return s
	}
	var b strings.Builder
	cols := 0
	for i := 0; i < len(s); {
		if loc := ansiRe.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			b.WriteString(s[i : i+loc[1]])
			i += loc[1]
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w := runewidth.RuneWidth(r)
		if cols+w > maxCols-1 { // reserve one column for the ellipsis
			break
		}
		b.WriteRune(r)
		cols += w
		i += size
	}
	return b.String() + "…" + ColorReset
}
