package components

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// TokenUsage mirrors tui.TokenUsage to avoid import cycle.
type TokenUsage struct {
	Used  int
	Limit int
	Cost  float64
}

// HeaderModel renders the top bar.
type HeaderModel struct {
	width int
}

func NewHeaderModel() HeaderModel {
	return HeaderModel{}
}

func (h *HeaderModel) SetWidth(w int) { h.width = w }

var (
	headerBgStyle = lipgloss.NewStyle().
		Background(lipgloss.Color("#1F2937")).
		Width(0) // will be set dynamically

	headerBrandStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7C3AED")).
		Background(lipgloss.Color("#1F2937")).
		Bold(true)

	headerInfoStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#D1D5DB")).
		Background(lipgloss.Color("#1F2937"))

	headerTokenStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#22D3EE")).
		Background(lipgloss.Color("#1F2937"))

	headerCostStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#F59E0B")).
		Background(lipgloss.Color("#1F2937"))

	headerDotStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280")).
		Background(lipgloss.Color("#1F2937"))
)

func (h HeaderModel) View(provider, model, session string, usage TokenUsage) string {
	dot := headerDotStyle.Render(" · ")

	// Left side: brand + model + provider
	left := " " + headerBrandStyle.Render("ChatCLI") + dot +
		headerInfoStyle.Render(model) + dot +
		headerInfoStyle.Render(provider)

	if session != "" {
		left += dot + headerInfoStyle.Render(session)
	}

	// Right side: tokens + cost
	var rightParts []string
	if usage.Used > 0 {
		tokenStr := formatTokens(usage.Used)
		if usage.Limit > 0 {
			pct := usage.Used * 100 / usage.Limit
			tokenStr += fmt.Sprintf(" (%d%%)", pct)
		}
		rightParts = append(rightParts, headerTokenStyle.Render(tokenStr+" tokens"))
	}
	if usage.Cost > 0 {
		rightParts = append(rightParts, headerCostStyle.Render(fmt.Sprintf("$%.4f", usage.Cost)))
	}

	right := ""
	for i, p := range rightParts {
		if i > 0 {
			right += dot
		}
		right += p
	}
	right += " "

	gap := h.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	bgStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1F2937")).
		Width(h.width)

	return bgStyle.Render(left + spaces(gap) + right)
}

func formatTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}
