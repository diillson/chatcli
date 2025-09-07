package cli

import "fmt"

// ANSI Color Codes
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorLime   = "\033[92m"
	ColorCyan   = "\033[36m"
	ColorGray   = "\033[90m"
	ColorPurple = "\033[35m"
	ColorBold   = "\033[1m"
)

// colorize aplica uma cor a uma string para uso geral com fmt.Print.
func colorize(text string, color string) string {
	return fmt.Sprintf("%s%s%s", color, text, ColorReset)
}
