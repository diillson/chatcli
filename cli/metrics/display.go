// Go Multi-Agent - Metrics Display
/*
 * ChatCLI - CLI metrics
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
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

func FormatDurationShort(d time.Duration) string { return d.Round(time.Second).String() }
func FormatDuration(d time.Duration) string      { return d.Round(time.Second).String() }

func FormatTimerStatus(d time.Duration, model, msg string) string {
	spinner := GetSpinnerFrame()
	dots := GetDotsAnimation()
	return fmt.Sprintf("\r%s%s%s [%s%s%s%s] %s[%s]%s %s|%s %s%s%s%s", ColorCyan, spinner, ColorReset, ColorBold, ColorCyan, model, ColorReset, ColorGray, FormatDurationShort(d), ColorReset, ColorGray, ColorReset, ColorGray, msg, dots, ColorReset)
}
func FormatTimerComplete(d time.Duration) string {
	return fmt.Sprintf("%s%s %s", ColorGray, FormatDuration(d), ColorReset)
}

// TurnStats holds accumulated session counters displayed alongside turn info.
type TurnStats struct {
	AgentsLaunched int
	ToolCallsExecd int
}

func FormatTurnInfo(t, m int, d time.Duration, stats *TurnStats) string {
	p := []string{fmt.Sprintf("%sTurn %d/%d%s", ColorCyan, t, m, ColorReset)}
	if d > 0 {
		p = append(p, FormatTimerComplete(d))
	}
	if stats != nil {
		var parts []string
		if stats.AgentsLaunched > 0 {
			label := "agent"
			if stats.AgentsLaunched > 1 {
				label = "agents"
			}
			parts = append(parts, fmt.Sprintf("%d %s", stats.AgentsLaunched, label))
		}
		if stats.ToolCallsExecd > 0 {
			label := "tool call"
			if stats.ToolCallsExecd > 1 {
				label = "tool calls"
			}
			parts = append(parts, fmt.Sprintf("%d %s", stats.ToolCallsExecd, label))
		}
		if len(parts) > 0 {
			p = append(p, fmt.Sprintf("%s[%s]%s", ColorGray, strings.Join(parts, ", "), ColorReset))
		}
	}
	return strings.Join(p, " ")
}

func ClearLine() string { return "\r\033[K" }
