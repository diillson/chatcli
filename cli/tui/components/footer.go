package components

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	ftrBg = lipgloss.Color("#171717")

	ftrPath = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280")).
		Background(ftrBg)

	ftrKey = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#4B5563")).
		Background(ftrBg)

	ftrHint = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#374151")).
		Background(ftrBg)
)

// FooterModel renders the bottom status bar.
type FooterModel struct {
	width int
}

func NewFooterModel() FooterModel {
	return FooterModel{}
}

func (f *FooterModel) SetWidth(w int) { f.width = w }

func (f FooterModel) View(cwd string, sidebarVisible, processing bool) string {
	short := shortenPath(cwd)
	left := " " + ftrPath.Render(short)

	var hints []string
	if processing {
		hints = append(hints, ftrKey.Render("^C")+ftrHint.Render(" cancel"))
	}
	if sidebarVisible {
		hints = append(hints, ftrKey.Render("^B")+ftrHint.Render(" sidebar"))
	} else {
		hints = append(hints, ftrKey.Render("^B")+ftrHint.Render(" sidebar"))
	}
	hints = append(hints,
		ftrKey.Render("^D")+ftrHint.Render(" quit"),
		ftrKey.Render("Tab")+ftrHint.Render(" complete"),
	)

	right := strings.Join(hints, ftrHint.Render("  ")) + " "

	gap := f.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	bgStyle := lipgloss.NewStyle().
		Background(ftrBg).
		Width(f.width)

	return bgStyle.Render(left + spaces(gap) + right)
}

func shortenPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(p, home) {
			p = "~" + p[len(home):]
		}
	}
	if len(p) > 50 {
		parts := strings.Split(p, string(filepath.Separator))
		if len(parts) > 3 {
			p = parts[0] + "/.../" + strings.Join(parts[len(parts)-2:], "/")
		}
	}
	return p
}
