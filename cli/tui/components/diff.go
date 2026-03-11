/*
 * ChatCLI - Diff Viewer Component
 * cli/tui/components/diff.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const defaultMaxDiffLines = 200

var (
	diffAddStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22C55E"))

	diffDelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#EF4444"))

	diffHunkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22D3EE"))

	diffHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E5E7EB"))

	diffContextStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#D1D5DB"))

	diffTruncatedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B7280")).
				Italic(true)
)

// DiffModel renders a unified diff with syntax highlighting.
type DiffModel struct {
	width        int
	maxLines     int
}

// NewDiffModel creates a DiffModel with default settings.
func NewDiffModel() DiffModel {
	return DiffModel{
		maxLines: defaultMaxDiffLines,
	}
}

// SetWidth sets the available width for rendering.
func (d *DiffModel) SetWidth(w int) {
	d.width = w
}

// SetMaxLines overrides the maximum number of lines shown before truncation.
func (d *DiffModel) SetMaxLines(n int) {
	if n > 0 {
		d.maxLines = n
	}
}

// RenderDiff takes a unified diff string and returns a styled string.
func (d DiffModel) RenderDiff(diff string) string {
	if diff == "" {
		return diffTruncatedStyle.Render("(no diff)")
	}

	lines := strings.Split(diff, "\n")
	totalLines := len(lines)
	truncated := false

	if totalLines > d.maxLines {
		lines = lines[:d.maxLines]
		truncated = true
	}

	var sb strings.Builder
	for i, line := range lines {
		styled := d.styleLine(line)
		sb.WriteString(styled)
		if i < len(lines)-1 {
			sb.WriteString("\n")
		}
	}

	if truncated {
		remaining := totalLines - d.maxLines
		sb.WriteString("\n")
		sb.WriteString(diffTruncatedStyle.Render(fmt.Sprintf("... +%d more lines", remaining)))
	}

	return sb.String()
}

// RenderFileDiff renders a diff for a specific file with a header.
func (d DiffModel) RenderFileDiff(filename, diff string) string {
	var sb strings.Builder

	header := diffHeaderStyle.Render(fmt.Sprintf("File: %s", filename))
	sb.WriteString(header)
	sb.WriteString("\n")

	w := d.width
	if w < 10 {
		w = 40
	}
	sb.WriteString(lipgloss.NewStyle().
		Foreground(lipgloss.Color("#374151")).
		Render(strings.Repeat("─", w)))
	sb.WriteString("\n")

	sb.WriteString(d.RenderDiff(diff))

	return sb.String()
}

// styleLine applies syntax highlighting to a single diff line.
func (d DiffModel) styleLine(line string) string {
	maxW := d.width
	if maxW > 0 && len(line) > maxW {
		line = line[:maxW]
	}

	switch {
	case strings.HasPrefix(line, "+++"):
		return diffHeaderStyle.Render(line)
	case strings.HasPrefix(line, "---"):
		return diffHeaderStyle.Render(line)
	case strings.HasPrefix(line, "@@"):
		return diffHunkStyle.Render(line)
	case strings.HasPrefix(line, "+"):
		return diffAddStyle.Render(line)
	case strings.HasPrefix(line, "-"):
		return diffDelStyle.Render(line)
	default:
		return diffContextStyle.Render(line)
	}
}
