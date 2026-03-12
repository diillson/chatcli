package tui

import tea "github.com/charmbracelet/bubbletea"

// listenNext returns a tea.Cmd that reads the next event from the channel.
// It chains itself: each event triggers another listenNext to read the next one.
// For TextDelta events, it batches all immediately available tokens into one message.
func listenNext(ch <-chan Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return StreamDoneMsg{}
		}
		// Batch consecutive TextDelta events to reduce render cycles
		if evt.Type == EventTextDelta {
			for {
				select {
				case next, ok := <-ch:
					if !ok {
						// Channel closed — deliver what we have, then Done on next call
						return BackendEventMsg{Event: evt}
					}
					if next.Type == EventTextDelta {
						evt.Text += next.Text
					} else {
						// Non-text event — deliver batched text, then the other event
						return BatchEventMsg{Events: []Event{evt, next}}
					}
				default:
					// No more events ready — deliver what we have
					return BackendEventMsg{Event: evt}
				}
			}
		}
		return BackendEventMsg{Event: evt}
	}
}

// BatchEventMsg delivers multiple events at once (batched text + following event).
type BatchEventMsg struct {
	Events []Event
}
