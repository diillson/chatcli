// Go Multi-Agent - Metrics Display
package metrics

import (
	"fmt"
	"strings"
	"time"
)

const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorCyan   = "\033[36m"
	ColorGray   = "\033[90m"
	ColorBold   = "\033[1m"
)

// ProgressBar gera barra de progresso com caracteres seguros
func ProgressBar(percent float64, width int) string {
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}
	filled := int(percent / 100 * float64(width))
	empty := width - filled
	color := ColorGreen
	if percent >= 90 {
		color = ColorRed
	} else if percent >= 70 {
		color = ColorYellow
	}
	return color + strings.Repeat("=", filled) + ColorGray + strings.Repeat("-", empty) + ColorReset
}

func FormatDurationShort(d time.Duration) string { return d.Round(time.Second).String() }
func FormatDuration(d time.Duration) string      { return d.Round(time.Second).String() }

func FormatTokenStatus(tc *TokenCounter) string {
	total := tc.GetTotalTokens()
	limit := tc.GetModelLimit()
	perc := tc.GetUsagePercent()
	bar := ProgressBar(perc, 20)
	mark := "OK"
	if perc >= 90 {
		mark = "CRIT"
	} else if perc >= 70 {
		mark = "WARN"
	}
	return fmt.Sprintf("[%s%s%s] Mem: %s/%s (%.1f%%) %s", mark, ColorCyan, ColorReset, FormatTokens(total), FormatTokens(limit), perc, bar)
}

func FormatTokenStatusCompact(tc *TokenCounter) string {
	total := tc.GetTotalTokens()
	limit := tc.GetModelLimit()
	perc := tc.GetUsagePercent()
	color := ColorGreen
	if perc >= 90 {
		color = ColorRed
	} else if perc >= 70 {
		color = ColorYellow
	}
	return fmt.Sprintf("%s%s/%s%s", color, FormatTokens(total), FormatTokens(limit), ColorReset)
}

func FormatTimerStatus(d time.Duration, model, msg string) string {
	spinner := GetSpinnerFrame()
	dots := GetDotsAnimation()
	return fmt.Sprintf("\r%s%s%s [%s%s%s%s] %s[%s]%s %s|%s %s%s%s%s", ColorCyan, spinner, ColorReset, ColorBold, ColorCyan, model, ColorReset, ColorGray, FormatDurationShort(d), ColorReset, ColorGray, ColorReset, ColorGray, msg, dots, ColorReset)
}
func FormatTimerComplete(d time.Duration) string {
	return fmt.Sprintf("%s%s %s", ColorGray, FormatDuration(d), ColorReset)
}

func FormatTurnInfo(t, m int, d time.Duration, tc *TokenCounter) string {
	p := []string{fmt.Sprintf("%sTurn %d/%d%s", ColorCyan, t, m, ColorReset)}
	if d > 0 {
		p = append(p, FormatTimerComplete(d))
	}
	if tc != nil {
		p = append(p, "["+FormatTokenStatusCompact(tc)+"]")
	}
	return strings.Join(p, " ")
}

func FormatWarning(msg string) string {
	return fmt.Sprintf("%s%s%s: %s%s", ColorYellow, ColorBold, "WARN", msg, ColorReset)
}

func ClearLine() string { return "\r\033[K" }
