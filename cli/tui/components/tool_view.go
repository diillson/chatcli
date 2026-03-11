/*
 * ChatCLI - Tool Call View Component
 * cli/tui/components/tool_view.go
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package components

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	toolBoxStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7C3AED")).
		Padding(0, 1)

	toolNameStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#F59E0B"))

	toolArgsStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#9CA3AF"))

	toolOutputStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#D1D5DB"))

	toolSuccessStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#22C55E"))

	toolErrorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#EF4444"))

	toolDurationStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280"))
)

// ToolViewModel renders tool call cards for the viewport.
type ToolViewModel struct {
	width int
}

// NewToolViewModel creates a new tool view model.
func NewToolViewModel() ToolViewModel {
	return ToolViewModel{}
}

// SetWidth sets the available rendering width.
func (t *ToolViewModel) SetWidth(w int) {
	t.width = w
}

// RenderToolStart renders a tool call that is currently executing.
func (t ToolViewModel) RenderToolStart(name, args string) string {
	contentWidth := t.width - 6
	if contentWidth < 20 {
		contentWidth = 20
	}

	header := "⏳ " + toolNameStyle.Render(name)

	var sb strings.Builder
	sb.WriteString(header)

	if args != "" {
		displayArgs := truncateToolArgs(args, contentWidth)
		sb.WriteString("\n")
		sb.WriteString(toolArgsStyle.Render(displayArgs))
	}

	return toolBoxStyle.Width(contentWidth).Render(sb.String())
}

// RenderToolResult renders a completed tool call with its result.
func (t ToolViewModel) RenderToolResult(name string, exitCode int, output string, duration time.Duration) string {
	contentWidth := t.width - 6
	if contentWidth < 20 {
		contentWidth = 20
	}

	// Status icon and color
	var statusIcon string
	var statusStyle lipgloss.Style
	if exitCode == 0 {
		statusIcon = "✅"
		statusStyle = toolSuccessStyle
	} else {
		statusIcon = "❌"
		statusStyle = toolErrorStyle
	}

	// Header line: icon + name + duration
	header := fmt.Sprintf("%s %s", statusIcon, toolNameStyle.Render(name))
	if duration > 0 {
		header += " " + toolDurationStyle.Render(fmt.Sprintf("(%s)", duration.Round(time.Millisecond)))
	}

	var sb strings.Builder
	sb.WriteString(header)

	// Exit code if non-zero
	if exitCode != 0 {
		sb.WriteString("\n")
		sb.WriteString(statusStyle.Render(fmt.Sprintf("exit %d", exitCode)))
	}

	// Output preview
	if output != "" {
		displayOutput := truncateToolOutput(output, contentWidth, 8)
		sb.WriteString("\n")
		sb.WriteString(toolOutputStyle.Render(displayOutput))
	}

	return toolBoxStyle.Width(contentWidth).Render(sb.String())
}

// RenderToolCallCompact renders a one-line summary of a tool call.
func (t ToolViewModel) RenderToolCallCompact(name string, exitCode int, duration time.Duration) string {
	icon := "✅"
	if exitCode != 0 {
		icon = "❌"
	}
	dur := ""
	if duration > 0 {
		dur = " " + toolDurationStyle.Render(fmt.Sprintf("(%s)", duration.Round(time.Millisecond)))
	}
	return fmt.Sprintf("%s %s%s", icon, toolNameStyle.Render(name), dur)
}

// truncateToolArgs shortens tool arguments for display.
func truncateToolArgs(args string, maxWidth int) string {
	// Remove newlines for compact display
	args = strings.ReplaceAll(args, "\n", " ")
	args = strings.Join(strings.Fields(args), " ")

	if len(args) > maxWidth {
		if maxWidth > 3 {
			return args[:maxWidth-3] + "..."
		}
		return args[:maxWidth]
	}
	return args
}

// truncateToolOutput shortens tool output, keeping first and last lines.
func truncateToolOutput(output string, maxWidth, maxLines int) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	if len(lines) <= maxLines {
		// Truncate each line to maxWidth
		var result []string
		for _, line := range lines {
			if len(line) > maxWidth {
				line = line[:maxWidth-3] + "..."
			}
			result = append(result, line)
		}
		return strings.Join(result, "\n")
	}

	// Show first few and last line with "... N more lines" in between
	showTop := maxLines - 2
	if showTop < 1 {
		showTop = 1
	}
	var result []string
	for i := 0; i < showTop; i++ {
		line := lines[i]
		if len(line) > maxWidth {
			line = line[:maxWidth-3] + "..."
		}
		result = append(result, line)
	}
	result = append(result, toolDurationStyle.Render(fmt.Sprintf("... %d more lines ...", len(lines)-showTop-1)))
	lastLine := lines[len(lines)-1]
	if len(lastLine) > maxWidth {
		lastLine = lastLine[:maxWidth-3] + "..."
	}
	result = append(result, lastLine)
	return strings.Join(result, "\n")
}
