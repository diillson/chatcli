package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap defines all key bindings for the TUI.
type KeyMap struct {
	Submit        key.Binding
	NewLine       key.Binding
	Cancel        key.Binding
	Quit          key.Binding
	ScrollUp      key.Binding
	ScrollDown    key.Binding
	ToggleSidebar key.Binding
	Rewind        key.Binding
	TabComplete   key.Binding
	HistoryUp     key.Binding
	HistoryDown   key.Binding
	ApproveYes    key.Binding
	ApproveNo     key.Binding
	ApproveAlways key.Binding
	ApproveSkip   key.Binding
	Help          key.Binding
}

// DefaultKeyMap returns the default key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "send"),
		),
		NewLine: key.NewBinding(
			key.WithKeys("shift+enter", "alt+enter"),
			key.WithHelp("shift+enter", "new line"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "cancel/clear"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "quit"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("pgup", "ctrl+u"),
			key.WithHelp("pgup", "scroll up"),
		),
		ScrollDown: key.NewBinding(
			key.WithKeys("pgdown", "ctrl+j"),
			key.WithHelp("pgdown", "scroll down"),
		),
		ToggleSidebar: key.NewBinding(
			key.WithKeys("ctrl+b"),
			key.WithHelp("ctrl+b", "toggle sidebar"),
		),
		Rewind: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc esc", "rewind"),
		),
		TabComplete: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "complete"),
		),
		HistoryUp: key.NewBinding(
			key.WithKeys("up"),
			key.WithHelp("up", "history prev"),
		),
		HistoryDown: key.NewBinding(
			key.WithKeys("down"),
			key.WithHelp("down", "history next"),
		),
		ApproveYes: key.NewBinding(
			key.WithKeys("y", "Y"),
			key.WithHelp("y", "approve"),
		),
		ApproveNo: key.NewBinding(
			key.WithKeys("n", "N"),
			key.WithHelp("n", "deny"),
		),
		ApproveAlways: key.NewBinding(
			key.WithKeys("a", "A"),
			key.WithHelp("a", "always"),
		),
		ApproveSkip: key.NewBinding(
			key.WithKeys("s", "S"),
			key.WithHelp("s", "skip"),
		),
		Help: key.NewBinding(
			key.WithKeys("?", "f1"),
			key.WithHelp("?", "help"),
		),
	}
}
