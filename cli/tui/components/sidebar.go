package components

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Task mirrors tui.Task to avoid import cycle.
type Task struct {
	Description string
	Status      string
}

// FileChange mirrors tui.FileChange to avoid import cycle.
type FileChange struct {
	Path      string
	Additions int
	Deletions int
}

// MCPServer mirrors tui.MCPServer to avoid import cycle.
type MCPServer struct {
	Name      string
	Connected bool
	ToolCount int
}

// ContextInfo mirrors tui.ContextInfo to avoid import cycle.
type ContextInfo struct {
	Name      string
	FileCount int
	SizeBytes int64
}

// SidebarModel renders the right panel.
type SidebarModel struct {
	width  int
	height int

	// Section collapse state
	TasksCollapsed bool
	FilesCollapsed bool
	MCPCollapsed   bool
}

func NewSidebarModel() SidebarModel {
	return SidebarModel{}
}

func (s *SidebarModel) SetSize(w, h int) {
	s.width = w
	s.height = h
}

var (
	sidebarTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#9CA3AF")).
				MarginBottom(0)

	sidebarMutedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#4B5563"))

	sidebarItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#D1D5DB"))

	sidebarValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#22D3EE"))

	sidebarAddStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#34D399"))

	sidebarDelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F87171"))

	sidebarConnStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#34D399"))

	sidebarDiscStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#EF4444"))

	sidebarSectionSep = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#374151"))
)

func (s SidebarModel) View(usage TokenUsage, tasks []Task, files []FileChange, mcpServers []MCPServer, session string, contexts []ContextInfo) string {
	style := lipgloss.NewStyle().
		Width(s.width).
		Height(s.height).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(lipgloss.Color("#374151")).
		Padding(0, 1)

	contentWidth := s.width - 4 // border + padding
	if contentWidth < 10 {
		contentWidth = 10
	}

	var sb strings.Builder

	// ── Session ──
	sb.WriteString(sidebarTitleStyle.Render("Session"))
	sb.WriteString("\n")
	if session != "" {
		sb.WriteString(sidebarItemStyle.Render(truncate(session, contentWidth)))
	} else {
		sb.WriteString(sidebarMutedStyle.Render("(unsaved)"))
	}
	sb.WriteString("\n")
	sb.WriteString(sidebarSectionSep.Render(strings.Repeat("─", contentWidth)))
	sb.WriteString("\n")

	// ── Attached Contexts ──
	if len(contexts) > 0 {
		header := fmt.Sprintf("Attached (%d)", len(contexts))
		sb.WriteString(sidebarTitleStyle.Render(header))
		sb.WriteString("\n")
		maxShow := len(contexts)
		if maxShow > 5 {
			maxShow = 5
		}
		for i := 0; i < maxShow; i++ {
			c := contexts[i]
			name := truncate(c.Name, contentWidth-12)
			size := formatSize(c.SizeBytes)
			sb.WriteString(fmt.Sprintf("  %s %s %s\n",
				sidebarValueStyle.Render(name),
				sidebarMutedStyle.Render(fmt.Sprintf("(%d files)", c.FileCount)),
				sidebarMutedStyle.Render(size)))
		}
		if len(contexts) > maxShow {
			sb.WriteString(sidebarMutedStyle.Render(fmt.Sprintf("  +%d more", len(contexts)-maxShow)))
			sb.WriteString("\n")
		}
		sb.WriteString(sidebarSectionSep.Render(strings.Repeat("─", contentWidth)))
		sb.WriteString("\n")
	}

	// ── Context / Tokens ──
	sb.WriteString(sidebarTitleStyle.Render("Tokens"))
	sb.WriteString("\n")
	total := usage.Used
	if usage.Limit > 0 {
		pct := total * 100 / usage.Limit
		bar := renderBar(pct, contentWidth-6) // leave room for percentage
		sb.WriteString(sidebarValueStyle.Render(fmt.Sprintf("%s", formatTokens(total))))
		sb.WriteString(sidebarMutedStyle.Render(fmt.Sprintf(" / %s tokens", formatTokens(usage.Limit))))
		sb.WriteString("\n")
		sb.WriteString(bar)
		sb.WriteString("\n")
	} else if total > 0 {
		sb.WriteString(sidebarValueStyle.Render(formatTokens(total)))
		sb.WriteString(sidebarMutedStyle.Render(" tokens"))
		sb.WriteString("\n")
	} else {
		sb.WriteString(sidebarMutedStyle.Render("0 tokens"))
		sb.WriteString("\n")
	}
	if usage.Cost > 0 {
		sb.WriteString(sidebarMutedStyle.Render(fmt.Sprintf("~$%.4f spent", usage.Cost)))
		sb.WriteString("\n")
	}
	sb.WriteString(sidebarSectionSep.Render(strings.Repeat("─", contentWidth)))
	sb.WriteString("\n")

	// ── Tasks ──
	if len(tasks) > 0 {
		header := fmt.Sprintf("Tasks (%d)", len(tasks))
		if s.TasksCollapsed && len(tasks) > 3 {
			sb.WriteString(sidebarTitleStyle.Render(header + " ▸"))
			sb.WriteString("\n")
		} else {
			sb.WriteString(sidebarTitleStyle.Render(header))
			sb.WriteString("\n")
			maxShow := len(tasks)
			if maxShow > 8 {
				maxShow = 8
			}
			for i := 0; i < maxShow; i++ {
				t := tasks[i]
				icon := taskIcon(t.Status)
				desc := truncate(t.Description, contentWidth-3)
				sb.WriteString(fmt.Sprintf("%s %s\n", icon, sidebarItemStyle.Render(desc)))
			}
			if len(tasks) > maxShow {
				sb.WriteString(sidebarMutedStyle.Render(fmt.Sprintf("  +%d more", len(tasks)-maxShow)))
				sb.WriteString("\n")
			}
		}
		sb.WriteString(sidebarSectionSep.Render(strings.Repeat("─", contentWidth)))
		sb.WriteString("\n")
	}

	// ── Modified Files ──
	if len(files) > 0 {
		header := fmt.Sprintf("Modified (%d)", len(files))
		if s.FilesCollapsed && len(files) > 3 {
			sb.WriteString(sidebarTitleStyle.Render(header + " ▸"))
			sb.WriteString("\n")
		} else {
			sb.WriteString(sidebarTitleStyle.Render(header))
			sb.WriteString("\n")
			maxShow := len(files)
			if maxShow > 6 {
				maxShow = 6
			}
			for i := 0; i < maxShow; i++ {
				f := files[i]
				name := filepath.Base(f.Path)
				adds := sidebarAddStyle.Render(fmt.Sprintf("+%d", f.Additions))
				dels := sidebarDelStyle.Render(fmt.Sprintf("-%d", f.Deletions))
				sb.WriteString(fmt.Sprintf("%s %s %s\n",
					sidebarItemStyle.Render(truncate(name, contentWidth-10)),
					adds, dels))
			}
			if len(files) > maxShow {
				sb.WriteString(sidebarMutedStyle.Render(fmt.Sprintf("  +%d more", len(files)-maxShow)))
				sb.WriteString("\n")
			}
		}
		sb.WriteString(sidebarSectionSep.Render(strings.Repeat("─", contentWidth)))
		sb.WriteString("\n")
	}

	// ── MCP Servers ──
	if len(mcpServers) > 0 {
		header := fmt.Sprintf("MCP (%d)", len(mcpServers))
		if s.MCPCollapsed && len(mcpServers) > 3 {
			sb.WriteString(sidebarTitleStyle.Render(header + " ▸"))
			sb.WriteString("\n")
		} else {
			sb.WriteString(sidebarTitleStyle.Render(header))
			sb.WriteString("\n")
			maxShow := len(mcpServers)
			if maxShow > 6 {
				maxShow = 6
			}
			for i := 0; i < maxShow; i++ {
				srv := mcpServers[i]
				var status string
				if srv.Connected {
					status = sidebarConnStyle.Render("●")
				} else {
					status = sidebarDiscStyle.Render("○")
				}
				name := truncate(srv.Name, contentWidth-8)
				tools := sidebarMutedStyle.Render(fmt.Sprintf("(%d)", srv.ToolCount))
				sb.WriteString(fmt.Sprintf("%s %s %s\n", status, sidebarItemStyle.Render(name), tools))
			}
			if len(mcpServers) > maxShow {
				sb.WriteString(sidebarMutedStyle.Render(fmt.Sprintf("  +%d more", len(mcpServers)-maxShow)))
				sb.WriteString("\n")
			}
		}
	}

	return style.Render(sb.String())
}

func taskIcon(status string) string {
	switch status {
	case "done":
		return "\u2705" // check
	case "running":
		return "\u23f3" // hourglass
	case "error":
		return "\u274c" // cross
	default:
		return "\u25cb" // circle
	}
}

func renderBar(pct, width int) string {
	if width < 4 {
		width = 4
	}
	filled := width * pct / 100
	if filled > width {
		filled = width
	}
	empty := width - filled

	filledStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED"))
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#374151"))

	bar := filledStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", empty))

	pctStr := sidebarMutedStyle.Render(fmt.Sprintf(" %d%%", pct))
	return bar + pctStr
}

func formatSize(bytes int64) string {
	if bytes >= 1_000_000 {
		return fmt.Sprintf("%.1fMB", float64(bytes)/1_000_000)
	}
	if bytes >= 1_000 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1_000)
	}
	return fmt.Sprintf("%dB", bytes)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max < 4 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
