package tui

import "github.com/charmbracelet/lipgloss"

// Cyberpunk / Vercel-inspired dark theme with neon accents.
var (
	// Core palette
	colorPrimary   = lipgloss.Color("#A855F7") // purple-400 (neon violet)
	colorSecondary = lipgloss.Color("#818CF8") // indigo-400
	colorAccent    = lipgloss.Color("#22D3EE") // cyan-400 (neon cyan)
	colorSuccess   = lipgloss.Color("#34D399") // emerald-400
	colorWarning   = lipgloss.Color("#FBBF24") // amber-400
	colorError     = lipgloss.Color("#F87171") // red-400
	colorMuted     = lipgloss.Color("#4B5563") // gray-600
	colorSubtle    = lipgloss.Color("#6B7280") // gray-500
	colorText      = lipgloss.Color("#F3F4F6") // gray-100
	colorDim       = lipgloss.Color("#9CA3AF") // gray-400

	// Backgrounds
	colorBg       = lipgloss.Color("#0A0A0A") // near-black (Vercel)
	colorSurface  = lipgloss.Color("#171717") // neutral-900
	colorSurface2 = lipgloss.Color("#1C1C1C") // slightly lighter
	colorBorder   = lipgloss.Color("#262626") // neutral-800

	// Semantic backgrounds
	colorUserBg   = lipgloss.Color("#1E1B4B") // indigo-950
	colorAssistBg = lipgloss.Color("#0A0A0A") // same as bg
	colorToolBg   = lipgloss.Color("#0C1222") // dark blue-black
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
	SidebarTitle lipgloss.Style
	SidebarItem  lipgloss.Style
	SidebarMuted lipgloss.Style

	// Misc
	Spinner  lipgloss.Style
	Border   lipgloss.Style
	Muted    lipgloss.Style
	Bold     lipgloss.Style
	Approval lipgloss.Style
}{
	// Layout — clean Vercel-style bars
	Header: lipgloss.NewStyle().
		Foreground(colorText).
		Background(colorSurface).
		Padding(0, 1).
		Bold(true),

	Footer: lipgloss.NewStyle().
		Foreground(colorMuted).
		Background(colorSurface).
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

	// Messages — thin borders, dark backgrounds
	UserMsg: lipgloss.NewStyle().
		Foreground(colorText).
		Background(colorUserBg).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#3730A3")), // indigo-800

	AssistantMsg: lipgloss.NewStyle().
		Foreground(colorText).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBorder),

	ToolMsg: lipgloss.NewStyle().
		Foreground(colorDim).
		Background(colorToolBg).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#1E3A5F")), // dark cyan-blue

	ErrorMsg: lipgloss.NewStyle().
		Foreground(colorError).
		Padding(0, 1).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7F1D1D")), // red-900

	SystemMsg: lipgloss.NewStyle().
		Foreground(colorMuted).
		Italic(true).
		Padding(0, 1),

	// Message labels — neon tags
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
		Foreground(colorDim).
		Bold(true).
		Underline(true),

	SidebarItem: lipgloss.NewStyle().
		Foreground(colorText),

	SidebarMuted: lipgloss.NewStyle().
		Foreground(colorMuted),

	// Misc
	Spinner: lipgloss.NewStyle().
		Foreground(colorAccent),

	Border: lipgloss.NewStyle().
		BorderForeground(colorBorder),

	Muted: lipgloss.NewStyle().
		Foreground(colorSubtle),

	Bold: lipgloss.NewStyle().
		Bold(true).
		Foreground(colorText),

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
