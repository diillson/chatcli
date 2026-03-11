package components

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
	left := " " + short

	var hints []string
	if processing {
		hints = append(hints, "Ctrl+C cancel")
	}
	if sidebarVisible {
		hints = append(hints, "Ctrl+B hide sidebar")
	} else {
		hints = append(hints, "Ctrl+B sidebar")
	}
	hints = append(hints, "Ctrl+D quit", "? help")

	right := strings.Join(hints, "  ") + " "

	gap := f.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}

	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280")).
		Background(lipgloss.Color("#1F2937")).
		Width(f.width)

	return style.Render(left + spaces(gap) + right)
}

func shortenPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(p, home) {
			p = "~" + p[len(home):]
		}
	}
	if len(p) > 50 {
		// Show ~/../lastTwo
		parts := strings.Split(p, string(filepath.Separator))
		if len(parts) > 3 {
			p = parts[0] + "/.../" + strings.Join(parts[len(parts)-2:], "/")
		}
	}
	return p
}
