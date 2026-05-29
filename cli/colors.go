package cli

import (
	"fmt"

	"github.com/diillson/chatcli/ui/theme"
)

// ANSI Color Codes
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorLime   = "\033[92m"
	ColorCyan   = "\033[36m"
	ColorGray   = "\033[90m"
	ColorPurple = "\033[35m"
	ColorBold   = "\033[1m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorBlue   = "\033[34m"
)

// colorize aplica uma cor a uma string para uso geral com fmt.Print. O
// resultado passa por theme.Recolor: os códigos básicos legados adotam a hue
// do tema ativo sob o profile de cor atual, e a saída perde todo ANSI quando
// não há terminal colorido (pipe/CI) — tudo isso sem mudar os call sites.
func colorize(text string, color string) string {
	return theme.Recolor(fmt.Sprintf("%s%s%s", color, text, ColorReset))
}
