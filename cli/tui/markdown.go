package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

var mdRenderer *glamour.TermRenderer

func init() {
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(0),
	)
	if err == nil {
		mdRenderer = r
	}
}

// RenderMarkdown renders a markdown string to styled terminal output.
// Falls back to the raw text if rendering fails.
func RenderMarkdown(text string) string {
	if mdRenderer == nil || strings.TrimSpace(text) == "" {
		return text
	}
	out, err := mdRenderer.Render(text)
	if err != nil {
		return text
	}
	// Ensure ANSI reset at end
	if !strings.HasSuffix(out, "\033[0m") {
		out += "\033[0m"
	}
	return strings.TrimRight(out, "\n")
}
