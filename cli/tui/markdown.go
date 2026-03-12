package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

var (
	mdRenderer      *glamour.TermRenderer
	mdRendererWidth int
	mdMu            sync.Mutex
)

// getMarkdownRenderer returns a glamour renderer configured for the given width.
// It reuses the existing renderer if the width hasn't changed.
func getMarkdownRenderer(width int) *glamour.TermRenderer {
	if width <= 0 {
		width = 80
	}
	mdMu.Lock()
	defer mdMu.Unlock()
	if mdRenderer != nil && mdRendererWidth == width {
		return mdRenderer
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	mdRenderer = r
	mdRendererWidth = width
	return mdRenderer
}

// RenderMarkdown renders a markdown string to styled terminal output.
// Falls back to the raw text if rendering fails.
func RenderMarkdown(text string, width int) string {
	r := getMarkdownRenderer(width)
	if r == nil || strings.TrimSpace(text) == "" {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	// Ensure ANSI reset at end
	if !strings.HasSuffix(out, "\033[0m") {
		out += "\033[0m"
	}
	return strings.TrimRight(out, "\n")
}
