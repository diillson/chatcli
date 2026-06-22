/*
 * ChatCLI - /config compression mutator.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Exposes the content-aware compression layer (CCR) on the /config surface,
 * read-only panorama plus runtime mode switching:
 *
 *   /config compression               # status (mode, thresholds, CCR store, savings)
 *   /config compression off           # disable compression
 *   /config compression lossless      # only lossless reductions (no row/line dropping)
 *   /config compression lossy         # lossy-with-CCR (full reduction, reversible via @recall)
 *   /config compression stats         # session savings summary
 *
 * The mode switch takes effect immediately on the live layer (atomic) and also
 * sets CHATCLI_COMPRESSION so any rebuilt layer (e.g. the gateway) inherits it.
 * A hint points to .env for a permanent default; we never rewrite .env.
 */
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/compress"
	"github.com/diillson/chatcli/i18n"
)

// routeConfigCompression dispatches /config compression <sub> [args]. The
// "compression" token is stripped by routeConfigCommand; empty args is handled
// there too (shows the panorama).
func (cli *ChatCLI) routeConfigCompression(args []string) {
	if len(args) == 0 {
		cli.showConfigCompression()
		return
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "help", "-h", "--help":
		cli.printConfigCompressionUsage()
	case "status", "show":
		cli.showConfigCompression()
	case "stats":
		cli.showCompressionStats()
	case "off", "disable", "none":
		cli.setCompressionMode("off")
	case "lossless", "safe":
		cli.setCompressionMode("lossless")
	case "lossy", "lossy-with-ccr", "ccr", "on", "enable":
		cli.setCompressionMode("lossy-with-ccr")
	case "mode":
		if len(args) >= 2 {
			cli.setCompressionMode(args[1])
		} else {
			cli.showConfigCompression()
		}
	default:
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.compression.set_invalid", args[0]), ColorRed))
		fmt.Println(colorize("  "+i18n.T("cfg.compression.set_valid"), ColorGray))
	}
}

// setCompressionMode flips the live layer's mode and mirrors it to the env var.
func (cli *ChatCLI) setCompressionMode(modeStr string) {
	if cli.compressionLayer == nil {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.compression.unavailable"), ColorRed))
		return
	}
	m, ok := compress.ParseMode(modeStr)
	if !ok {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.compression.set_invalid", modeStr), ColorRed))
		fmt.Println(colorize("  "+i18n.T("cfg.compression.set_valid"), ColorGray))
		return
	}
	prev := cli.compressionLayer.Mode()
	cli.compressionLayer.SetMode(m)
	_ = os.Setenv("CHATCLI_COMPRESSION", m.String())

	if prev == m {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.compression.set_noop", m.String()), ColorGray))
	} else {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.compression.set_ok", prev.String(), m.String()), ColorGreen))
	}
	fmt.Println(colorize("    "+i18n.T("cfg.compression.persist_hint", m.String()), ColorGray))
}

// showConfigCompression renders the compression panorama.
func (cli *ChatCLI) showConfigCompression() {
	sectionHeader("🗜️", "cfg.section.compression.title", ColorBlue)
	p := uiPrefix(ColorBlue)

	mode := "off"
	if cli.compressionLayer != nil {
		mode = cli.compressionLayer.Mode().String()
	}
	kv(p, i18n.T("cfg.compression.mode"), mode)
	kv(p, "CHATCLI_COMPRESSION_THRESHOLD", envOrDefault("CHATCLI_COMPRESSION_THRESHOLD", "4000"))
	kv(p, "CHATCLI_COMPRESSION_CCR_DIR", envOrDefault("CHATCLI_COMPRESSION_CCR_DIR", "~/.chatcli/ccr"))
	kv(p, "CHATCLI_COMPRESSION_CCR_MAX_MB", envOrDefault("CHATCLI_COMPRESSION_CCR_MAX_MB", "256"))
	kv(p, "CHATCLI_COMPRESSION_CCR_TTL", envOrDefault("CHATCLI_COMPRESSION_CCR_TTL", "168h"))

	if cli.compressionLayer != nil {
		stats, store := cli.compressionLayer.Stats()
		kv(p, i18n.T("cfg.compression.saved"),
			fmt.Sprintf("%d/%d bytes (%.0f%%)", stats.SavedBytes(), stats.BytesIn, (1-stats.Ratio())*100))
		kv(p, i18n.T("cfg.compression.ccr_store"),
			fmt.Sprintf("%d entries / %d bytes", store.Entries, store.TotalBytes))
	}

	fmt.Println(p)
	fmt.Println(p + colorize(i18n.T("cfg.compression.about"), ColorGray))
	fmt.Println(p + colorize(i18n.T("cfg.compression.change_hint"), ColorGray))
	sectionEnd(ColorBlue)
}

// showCompressionStats prints the detailed per-strategy savings summary.
func (cli *ChatCLI) showCompressionStats() {
	if cli.compressionLayer == nil {
		fmt.Println(colorize("  "+i18n.T("cfg.compression.unavailable"), ColorYellow))
		return
	}
	sectionHeader("🗜️", "cfg.section.compression.title", ColorBlue)
	p := uiPrefix(ColorBlue)
	stats, store := cli.compressionLayer.Stats()

	kv(p, i18n.T("cfg.compression.calls"), fmt.Sprintf("%d (%d reduced)", stats.Calls, stats.Reductions))
	kv(p, i18n.T("cfg.compression.saved"),
		fmt.Sprintf("%d/%d bytes (%.0f%%)", stats.SavedBytes(), stats.BytesIn, (1-stats.Ratio())*100))
	kv(p, "CCR", fmt.Sprintf("%d stored / %d recalled / %d misses", stats.CCRPuts, stats.CCRHits, stats.CCRMisses))
	for _, s := range stats.ByStrategy {
		kv(p, "  "+s.Strategy, fmt.Sprintf("calls=%d  %d→%d bytes", s.Calls, s.BytesIn, s.BytesOut))
	}
	kv(p, i18n.T("cfg.compression.ccr_store"), fmt.Sprintf("%d entries / %d bytes", store.Entries, store.TotalBytes))
	sectionEnd(ColorBlue)
}

// printConfigCompressionUsage shows the subcommand cheat sheet.
func (cli *ChatCLI) printConfigCompressionUsage() {
	fmt.Println(colorize(i18n.T("cfg.compression.usage_header"), ColorCyan+ColorBold))
	fmt.Println("  /config compression            # " + i18n.T("cfg.compression.usage_status"))
	fmt.Println("  /config compression lossy      # " + i18n.T("cfg.compression.usage_lossy"))
	fmt.Println("  /config compression lossless   # " + i18n.T("cfg.compression.usage_lossless"))
	fmt.Println("  /config compression off        # " + i18n.T("cfg.compression.usage_off"))
	fmt.Println("  /config compression stats      # " + i18n.T("cfg.compression.usage_stats"))
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("cfg.compression.usage_note"), ColorGray))
}

// getConfigCompressionSuggestions autocompletes `/config compression …`.
func (cli *ChatCLI) getConfigCompressionSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()

	if len(args) == 2 || (len(args) == 3 && !strings.HasSuffix(line, " ")) {
		subs := []prompt.Suggest{
			{Text: "lossy", Description: i18n.T("complete.config.compression_lossy")},
			{Text: "lossless", Description: i18n.T("complete.config.compression_lossless")},
			{Text: "off", Description: i18n.T("complete.config.compression_off")},
			{Text: "stats", Description: i18n.T("complete.config.compression_stats")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}
	return []prompt.Suggest{}
}

// envOrDefault returns the env var value or a default when unset/empty.
func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
