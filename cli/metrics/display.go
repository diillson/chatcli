/*
 * ChatCLI - Metrics Display
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package metrics

import (
	"fmt"
	"strings"
	"time"
)

// Cores ANSI
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorCyan   = "\033[36m"
	ColorGray   = "\033[90m"
	ColorBold   = "\033[1m"
)

// ProgressBar gera uma barra de progresso visual
func ProgressBar(percent float64, width int) string {
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}

	filled := int(percent / 100 * float64(width))
	empty := width - filled

	// Escolhe a cor baseada na porcentagem
	var color string
	switch {
	case percent >= 90:
		color = ColorRed
	case percent >= 70:
		color = ColorYellow
	default:
		color = ColorGreen
	}

	bar := color + strings.Repeat("▬", filled) + ColorGray + strings.Repeat("░", empty) + ColorReset
	return bar
}

// FormatTokenStatus formata o status de tokens para exibição
func FormatTokenStatus(tc *TokenCounter) string {
	total := tc.GetTotalTokens()
	limit := tc.GetModelLimit()
	percent := tc.GetUsagePercent()

	bar := ProgressBar(percent, 20)

	// Escolhe emoji baseado no estado
	var emoji string
	switch {
	case percent >= 90:
		emoji = "🔴" // Vermelho
	case percent >= 70:
		emoji = "🟡" // Amarelo
	default:
		emoji = "🟢" // Verde
	}

	return fmt.Sprintf("%s Contexto: %s / %s (%.1f%%) %s",
		emoji,
		FormatTokens(total),
		FormatTokens(limit),
		percent,
		bar,
	)
}

// FormatTokenStatusCompact formata o status de forma compacta
func FormatTokenStatusCompact(tc *TokenCounter) string {
	total := tc.GetTotalTokens()
	limit := tc.GetModelLimit()
	percent := tc.GetUsagePercent()

	// Escolhe cor baseada no estado
	var color string
	switch {
	case percent >= 90:
		color = ColorRed
	case percent >= 70:
		color = ColorYellow
	default:
		color = ColorGreen
	}

	return fmt.Sprintf("%s[%s/%s]%s",
		color,
		FormatTokens(total),
		FormatTokens(limit),
		ColorReset,
	)
}

// Spinner frames
var SpinnerFrames = []string{"‖", "―", "‖", "‗", "‘", "’", "‚", "‛", "“"}

// FormatTimerStatus formata o status do timer durante execução
func FormatTimerStatus(d time.Duration, modelName string, message string) string {
	// Calcula o frame do spinner baseado no tempo (muda a ms 100ms)
	frameIndex := int(d.Milliseconds()/100) % len(SpinnerFrames)
	spinner := SpinnerFrames[frameIndex]

	return fmt.Sprintf("\r%s%s [%s%s%s] %s%s | %s%s",
		ColorCyan, spinner,
		ColorBold, modelName, ColorReset+ColorCyan,
		FormatDurationShort(d),
		ColorReset,
		message,
		strings.Repeat(" ", 10), // Limpa resíduos
	)
}

// FormatTimerComplete formata o tempo final de execução
func FormatTimerComplete(d time.Duration) string {
	return fmt.Sprintf("%s%s %s%s",
		ColorGray, "✑", // Check mark
		FormatDuration(d),
		ColorReset,
	)
}

// FormatTurnInfo formata informações do turno com barra de progresso
func FormatTurnInfo(turn int, maxTurns int, duration time.Duration, tc *TokenCounter) string {
	var parts []string

	// Turno
	parts = append(parts, fmt.Sprintf("%sTurno %d/%d%s",
		ColorCyan, turn, maxTurns, ColorReset))

	// Tempo
	if duration > 0 {
		parts = append(parts, FormatTimerComplete(duration))
	}

	// Tokens com barra de progresso
	if tc != nil {
		percent := tc.GetUsagePercent()
		bar := ProgressBar(percent, 15) // Barra de 15 caracteres
		parts = append(parts, FormatTokenStatusCompact(tc))
		parts = append(parts, bar)
	}

	return strings.Join(parts, " ")
}

// FormatWarning formata um aviso de tokens
func FormatWarning(message string) string {
	return fmt.Sprintf("%s%s⚠️  %s%s", ColorYellow, ColorBold, message, ColorReset)
}

// FormatError formata um erro
func FormatError(message string) string {
	return fmt.Sprintf("%s%s❌ %s%s", ColorRed, ColorBold, message, ColorReset)
}

// ClearLine limpa a linha atual do terminal
func ClearLine() string {
	return "\r\033[K"
}
