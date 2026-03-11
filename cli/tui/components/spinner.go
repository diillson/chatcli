package components

import (
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SpinnerTickMsg wraps spinner.TickMsg to export it.
type SpinnerTickMsg = spinner.TickMsg

// SpinnerModel wraps bubbles/spinner for the TUI.
type SpinnerModel struct {
	inner spinner.Model
}

func NewSpinnerModel() SpinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED"))
	return SpinnerModel{inner: s}
}

func (s SpinnerModel) Init() tea.Cmd {
	return s.inner.Tick
}

func (s SpinnerModel) Update(msg tea.Msg) (SpinnerModel, tea.Cmd) {
	var cmd tea.Cmd
	s.inner, cmd = s.inner.Update(msg)
	return s, cmd
}

func (s SpinnerModel) View() string {
	return s.inner.View()
}

func (s SpinnerModel) Tick() tea.Cmd {
	return s.inner.Tick
}
