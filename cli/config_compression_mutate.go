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
	"strconv"
	"strings"
	"time"

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
	case "prune", "gc", "cleanup", "curate":
		cli.pruneCompressionStore()
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
	case "profile":
		if len(args) >= 2 {
			cli.setCompressionProfile(args[1])
		} else {
			cli.showConfigCompression()
		}
	case "threshold":
		if len(args) >= 2 {
			cli.setCompressionThreshold(args[1])
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

// setCompressionProfile flips the live layer's aggressiveness profile and
// mirrors it to the env var so a rebuilt layer (e.g. the gateway) inherits it.
func (cli *ChatCLI) setCompressionProfile(profileStr string) {
	if cli.compressionLayer == nil {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.compression.unavailable"), ColorRed))
		return
	}
	p, ok := compress.ParseProfile(strings.ToLower(strings.TrimSpace(profileStr)))
	if !ok {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.compression.profile_invalid", profileStr), ColorRed))
		fmt.Println(colorize("  "+i18n.T("cfg.compression.profile_valid"), ColorGray))
		return
	}
	prev := cli.compressionLayer.Profile()
	cli.compressionLayer.SetProfile(p)
	_ = os.Setenv("CHATCLI_COMPRESSION_PROFILE", p.String())

	if prev == p {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.compression.profile_noop", p.String()), ColorGray))
	} else {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.compression.profile_ok", prev.String(), p.String()), ColorGreen))
	}
	fmt.Println(colorize("    "+i18n.T("cfg.compression.profile_persist_hint", p.String()), ColorGray))
}

// setCompressionThreshold changes the engage threshold (bytes) on the live
// layer and mirrors it to the env var for rebuilt layers.
func (cli *ChatCLI) setCompressionThreshold(raw string) {
	if cli.compressionLayer == nil {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.compression.unavailable"), ColorRed))
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.compression.threshold_invalid", raw), ColorRed))
		return
	}
	prev := cli.compressionLayer.Threshold()
	cli.compressionLayer.SetThreshold(n)
	_ = os.Setenv("CHATCLI_COMPRESSION_THRESHOLD", strconv.Itoa(n))

	if prev == n {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.compression.threshold_noop", n), ColorGray))
	} else {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.compression.threshold_ok", prev, n), ColorGreen))
	}
}

// pruneCompressionStore runs an on-demand curation pass over the CCR store
// (TTL-expired entries + size-cap eviction) and reports what was freed, so the
// user has an explicit lever instead of relying on the startup/throttled sweeps.
func (cli *ChatCLI) pruneCompressionStore() {
	if cli.compressionLayer == nil {
		fmt.Println(colorize("  ❌ "+i18n.T("cfg.compression.unavailable"), ColorRed))
		return
	}
	res := cli.compressionLayer.Prune()
	if res.Removed == 0 {
		fmt.Println(colorize("  ✔ "+i18n.T("cfg.compression.prune_none", res.RemainingEntries), ColorGray))
		return
	}
	fmt.Println(colorize("  ✔ "+i18n.T("cfg.compression.prune_ok",
		res.Removed, formatTokenCount(res.BytesFreed), res.RemainingEntries), ColorGreen))
}

// ccrStoreSummary renders the CCR footprint with curation visibility: entries,
// bytes, and (when present) how many entries are already stale plus the oldest
// entry's age, so the store is never an opaque blob.
func ccrStoreSummary(store compress.StoreStats) string {
	out := fmt.Sprintf("%d entries / %d bytes", store.Entries, store.TotalBytes)
	if store.StaleEntries > 0 {
		out += fmt.Sprintf(" · %s", i18n.T("cfg.compression.ccr_stale", store.StaleEntries))
	}
	if store.OldestAge > 0 {
		out += fmt.Sprintf(" · %s", i18n.T("cfg.compression.ccr_oldest", formatAgeCompact(store.OldestAge)))
	}
	return out
}

// formatAgeCompact renders a duration as a short human age: "45m", "6h", "3d".
func formatAgeCompact(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
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
	profile := compress.ProfileDefault.String()
	threshold := fmt.Sprintf("%d", compress.DefaultThreshold)
	if cli.compressionLayer != nil {
		profile = cli.compressionLayer.Profile().String()
		threshold = fmt.Sprintf("%d", cli.compressionLayer.Threshold())
	}
	kv(p, i18n.T("cfg.compression.profile"), profile)
	kv(p, "CHATCLI_COMPRESSION_THRESHOLD", threshold)
	kv(p, "CHATCLI_COMPRESSION_CCR_DIR", envOrDefault("CHATCLI_COMPRESSION_CCR_DIR", "~/.chatcli/ccr"))
	kv(p, "CHATCLI_COMPRESSION_CCR_MAX_MB", envOrDefault("CHATCLI_COMPRESSION_CCR_MAX_MB", "256"))
	kv(p, "CHATCLI_COMPRESSION_CCR_TTL", envOrDefault("CHATCLI_COMPRESSION_CCR_TTL", "168h"))

	if cli.compressionLayer != nil {
		stats, store := cli.compressionLayer.Stats()
		kv(p, i18n.T("cfg.compression.saved"),
			fmt.Sprintf("%d/%d bytes (%.0f%%)", stats.SavedBytes(), stats.BytesIn, (1-stats.Ratio())*100))
		kv(p, i18n.T("cfg.compression.ccr_store"), ccrStoreSummary(store))
		if err := cli.compressionLayer.StoreFallback(); err != nil {
			kv(p, i18n.T("cfg.compression.store_backend"),
				colorize(i18n.T("cfg.compression.store_memory_fallback", err.Error()), ColorYellow))
		}
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
	ccrLine := fmt.Sprintf("%d stored / %d recalled / %d misses", stats.CCRPuts, stats.CCRHits, stats.CCRMisses)
	if rate, ok := stats.RecallHitRate(); ok {
		ccrLine += " · " + i18n.T("cfg.compression.ccr_hitrate", rate)
	}
	kv(p, "CCR", ccrLine)
	for _, s := range stats.ByStrategy {
		kv(p, "  "+s.Strategy, fmt.Sprintf("calls=%d  %d→%d bytes", s.Calls, s.BytesIn, s.BytesOut))
	}
	kv(p, i18n.T("cfg.compression.ccr_store"), ccrStoreSummary(store))
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
	fmt.Println("  /config compression prune      # " + i18n.T("cfg.compression.usage_prune"))
	fmt.Println("  /config compression profile <conservative|default|aggressive>  # " + i18n.T("cfg.compression.usage_profile"))
	fmt.Println("  /config compression threshold <bytes>                          # " + i18n.T("cfg.compression.usage_threshold"))
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
			{Text: "prune", Description: i18n.T("complete.config.compression_prune")},
			{Text: "profile", Description: i18n.T("complete.config.compression_profile")},
			{Text: "threshold", Description: i18n.T("complete.config.compression_threshold")},
		}
		return prompt.FilterHasPrefix(subs, word, true)
	}

	// Third token: values for `/config compression profile <…>`.
	if len(args) >= 3 && strings.EqualFold(args[2], "profile") {
		subs := []prompt.Suggest{
			{Text: "conservative", Description: i18n.T("complete.config.compression_profile_conservative")},
			{Text: "default", Description: i18n.T("complete.config.compression_profile_default")},
			{Text: "aggressive", Description: i18n.T("complete.config.compression_profile_aggressive")},
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
