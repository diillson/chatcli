package tui

import "github.com/charmbracelet/lipgloss"

// Theme colors inspired by OpenCode's dark terminal aesthetic.
var (
	colorPrimary   = lipgloss.Color("#7C3AED") // violet
	colorSecondary = lipgloss.Color("#6366F1") // indigo
	colorAccent    = lipgloss.Color("#22D3EE") // cyan
	colorSuccess   = lipgloss.Color("#22C55E") // green
	colorWarning   = lipgloss.Color("#F59E0B") // amber
	colorError     = lipgloss.Color("#EF4444") // red
	colorMuted     = lipgloss.Color("#6B7280") // gray
	colorText      = lipgloss.Color("#E5E7EB") // light gray
	colorBg        = lipgloss.Color("#111827") // dark bg
	colorBorder    = lipgloss.Color("#374151") // border gray
	colorUserBg    = lipgloss.Color("#1E1B4B") // dark indigo
	colorAssistBg  = lipgloss.Color("#111827") // same as bg
	colorToolBg    = lipgloss.Color("#1A2332") // dark blue-gray
)

// Styles holds all lipgloss styles used across the TUI.
var Styles = struct {
	// Layout
	Header    lipgloss.Style
	Footer    lipgloss.Style
	Sidebar   lipgloss.Style
	Viewport  lipgloss.Style
	InputArea lipgloss.Style

	// Messages
	UserMsg      lipgloss.Style
	AssistantMsg lipgloss.Style
	ToolMsg      lipgloss.Style
	ErrorMsg     lipgloss.Style
	SystemMsg    lipgloss.Style

	// Message labels
	UserLabel      lipgloss.Style
	AssistantLabel lipgloss.Style
	ToolLabel      lipgloss.Style

	// Sidebar sections
	SidebarTitle   lipgloss.Style
	SidebarItem    lipgloss.Style
	SidebarMuted   lipgloss.Style

	// Misc
	Spinner  lipgloss.Style
	Border   lipgloss.Style
	Muted    lipgloss.Style
	Bold     lipgloss.Style
	Approval lipgloss.Style
}{
	// Layout
	Header: lipgloss.NewStyle().
		Foreground(colorText).
		Background(lipgloss.Color("#1F2937")).
		Padding(0, 1).
		Bold(true),

	Footer: lipgloss.NewStyle().
		Foreground(colorMuted).
		Background(lipgloss.Color("#1F2937")).
		Padding(0, 1),

	Sidebar: lipgloss.NewStyle().
		Foreground(colorText).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colorBorder).
		Padding(0, 1),

	Viewport: lipgloss.NewStyle().
		Foreground(colorText),

	InputArea: lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(colorBorder).
		Padding(0, 1),

	// Messages
	UserMsg: lipgloss.NewStyle().
		Foreground(colorText).
		Background(colorUserBg).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorSecondary),

	AssistantMsg: lipgloss.NewStyle().
		Foreground(colorText).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary),

	ToolMsg: lipgloss.NewStyle().
		Foreground(colorText).
		Background(colorToolBg).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent),

	ErrorMsg: lipgloss.NewStyle().
		Foreground(colorError).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorError),

	SystemMsg: lipgloss.NewStyle().
		Foreground(colorMuted).
		Italic(true).
		Padding(0, 1),

	// Message labels
	UserLabel: lipgloss.NewStyle().
		Foreground(colorSecondary).
		Bold(true),

	AssistantLabel: lipgloss.NewStyle().
		Foreground(colorPrimary).
		Bold(true),

	ToolLabel: lipgloss.NewStyle().
		Foreground(colorAccent).
		Bold(true),

	// Sidebar
	SidebarTitle: lipgloss.NewStyle().
		Foreground(colorText).
		Bold(true).
		Underline(true),

	SidebarItem: lipgloss.NewStyle().
		Foreground(colorText),

	SidebarMuted: lipgloss.NewStyle().
		Foreground(colorMuted),

	// Misc
	Spinner: lipgloss.NewStyle().
		Foreground(colorPrimary),

	Border: lipgloss.NewStyle().
		BorderForeground(colorBorder),

	Muted: lipgloss.NewStyle().
		Foreground(colorMuted),

	Bold: lipgloss.NewStyle().
		Bold(true),

	Approval: lipgloss.NewStyle().
		Foreground(colorWarning).
		Bold(true).
		Padding(1, 2).
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorWarning),
}

// Minimum width for sidebar to be visible alongside viewport.
const SidebarBreakpoint = 120
const SidebarWidth = 42
