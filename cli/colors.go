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

	// Marcadores SOH (Start of Heading) e STX (Start of Text) para o liner.
	// Estes são os caracteres de controle que dizem ao readline para não contar
	// os bytes entre eles como parte da largura do prompt visível.
	// Em Go, \x01 e \x02 são as representações corretas.
	ignoreStart = "\x01"
	ignoreEnd   = "\x02"
)

// colorize aplica uma cor a uma string para uso geral com fmt.Print.
func colorize(text string, color string) string {
	return fmt.Sprintf("%s%s%s", color, text, ColorReset)
}

// colorizeForPrompt envolve os códigos de cor com marcadores de escape
// para que a biblioteca 'liner' calcule o comprimento do prompt corretamente.
func colorizeForPrompt(text string, color string) string {
	return fmt.Sprintf("%s%s%s%s%s%s", ignoreStart, color, ignoreEnd, text, ignoreStart, ColorReset+ignoreEnd)
}
