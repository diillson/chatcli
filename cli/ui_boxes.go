package cli

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/ui/kit"
)

// ─── Terminal Box Helpers ──────────────────────────────────────
// Shared UI functions for rendering consistent boxed output
// across /mcp, /hooks, /cost, /channel, /worktree, etc.
// Width and truncation math delegate to ui/kit so every surface
// agrees on geometry; these helpers survive as the legacy color-
// constant facade until the /config surfaces migrate to kit
// components directly.

func uiTermWidth() int {
	return kit.TermWidth()
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

// ansiTruncate clamps s to at most maxCols visible columns, preserving ANSI
// sequences and appending an ellipsis when content was dropped. Logic in
// kit.Truncate.
func ansiTruncate(s string, maxCols int) string {
	return kit.Truncate(s, maxCols)
}
