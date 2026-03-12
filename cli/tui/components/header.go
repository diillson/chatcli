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
	hdrBg = lipgloss.Color("#171717")

	hdrBrand = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A855F7")).
			Background(hdrBg).
			Bold(true)

	hdrSep = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#262626")).
		Background(hdrBg)

	hdrInfo = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#9CA3AF")).
		Background(hdrBg)

	hdrAccent = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22D3EE")).
			Background(hdrBg)

	hdrCost = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FBBF24")).
		Background(hdrBg)

	hdrMuted = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#4B5563")).
			Background(hdrBg)
)

func (h HeaderModel) View(provider, model, session string, usage TokenUsage) string {
	sep := hdrSep.Render(" │ ")

	// Left: brand + model + provider
	left := " " + hdrBrand.Render("▲ ChatCLI") + sep +
		hdrInfo.Render(model) + hdrMuted.Render("@") + hdrInfo.Render(provider)

	if session != "" {
		left += sep + hdrAccent.Render(session)
	}

	// Right: tokens + cost
	var rightParts []string
	if usage.Used > 0 {
		tokenStr := formatTokens(usage.Used)
		if usage.Limit > 0 {
			pct := usage.Used * 100 / usage.Limit
			tokenStr += fmt.Sprintf(" %d%%", pct)
		}
		rightParts = append(rightParts, hdrAccent.Render(tokenStr))
	}
	if usage.Cost > 0 {
		rightParts = append(rightParts, hdrCost.Render(fmt.Sprintf("$%.4f", usage.Cost)))
	}

	right := ""
	for i, p := range rightParts {
		if i > 0 {
			right += sep
		}
		right += p
	}
	right += " "

	gap := h.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	bgStyle := lipgloss.NewStyle().
		Background(hdrBg).
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
