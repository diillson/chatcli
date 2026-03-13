/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"runtime"
	"strconv"

	"github.com/diillson/chatcli/i18n"
)

// handleMetricsCommand displays runtime telemetry in the terminal.
func (ch *CommandHandler) handleMetricsCommand() {
	c := ch.cli

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("metrics.header"), ColorLime))
	fmt.Println()

	// Provider & Model
	model := ""
	if c.Client != nil {
		model = c.Client.GetModelName()
	}
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.provider"), ColorCyan), colorize(c.Provider, ColorGray))
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.model"), ColorCyan), colorize(model, ColorGray))

	// Session info
	sessionName := c.currentSessionName
	if sessionName == "" {
		sessionName = i18n.T("metrics.session.unsaved")
	}
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.session"), ColorCyan), colorize(sessionName, ColorGray))
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.history_msgs"), ColorCyan), colorize(strconv.Itoa(len(c.history)), ColorGray))

	// Token usage estimate
	tokenLimit := c.UserMaxTokens
	if tokenLimit <= 0 {
		tokenLimit = c.getMaxTokensForCurrentLLM()
	}
	tokenUsed := 0
	for _, msg := range c.history {
		tokenUsed += len(msg.Content) / 4 // rough estimate: ~4 chars per token
	}
	tokenPct := 0.0
	if tokenLimit > 0 {
		tokenPct = float64(tokenUsed) / float64(tokenLimit) * 100
	}
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.tokens"), ColorCyan),
		colorize(fmt.Sprintf("%d / %d (%.1f%%)", tokenUsed, tokenLimit, tokenPct), ColorGray))

	// Turn count
	turns := 0
	for _, msg := range c.history {
		if msg.Role == "user" {
			turns++
		}
	}
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.turns"), ColorCyan), colorize(strconv.Itoa(turns), ColorGray))

	// Go runtime
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("metrics.header.go_runtime"), ColorLime))
	fmt.Println()
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.goroutines"), ColorCyan), colorize(strconv.Itoa(runtime.NumGoroutine()), ColorGray))
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.alloc"), ColorCyan), colorize(formatBytes(m.Alloc), ColorGray))
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.sys"), ColorCyan), colorize(formatBytes(m.Sys), ColorGray))
	fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.gc_cycles"), ColorCyan), colorize(strconv.FormatUint(uint64(m.NumGC), 10), ColorGray))

	// Remote connection
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("metrics.header.connection"), ColorLime))
	fmt.Println()
	if c.isRemote {
		fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.remote"), ColorCyan), colorize(i18n.T("metrics.value.connected"), ColorGreen))
	} else {
		fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.remote"), ColorCyan), colorize(i18n.T("metrics.value.local"), ColorGray))
	}

	// Watcher
	if c.watchStatusFunc != nil {
		status := c.watchStatusFunc()
		if status != "" {
			fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.watcher"), ColorCyan), colorize(status, ColorGray))
		}
	} else {
		fmt.Printf("    %s    %s\n", colorize(i18n.T("metrics.label.watcher"), ColorCyan), colorize(i18n.T("metrics.value.inactive"), ColorGray))
	}

	fmt.Println()
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
