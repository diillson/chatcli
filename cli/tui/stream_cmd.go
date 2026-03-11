package tui

import tea "github.com/charmbracelet/bubbletea"

// listenNext returns a tea.Cmd that reads the next event from the channel.
// It chains itself: each event triggers another listenNext to read the next one.
func listenNext(ch <-chan Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return StreamDoneMsg{}
		}
		return BackendEventMsg{Event: evt}
	}
}
