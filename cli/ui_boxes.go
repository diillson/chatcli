package cli

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ─── Terminal Box Helpers ──────────────────────────────────────
// Shared UI functions for rendering consistent boxed output
// across /mcp, /hooks, /cost, /channel, /worktree, etc.

func uiTermWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
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
