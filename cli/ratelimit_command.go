/*
 * ChatCLI - ratelimit_command.go
 *
 * /ratelimit (alias /limits) shows the latest rate-limit state captured from
 * provider response headers. Data is populated centrally via
 * auth.ResponseObserver for every HTTP provider, so this works across all
 * providers without per-provider code.
 */
package cli

import (
	"fmt"
	"sort"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/ratelimit"
)

func (cli *ChatCLI) handleRateLimitCommand() {
	snaps := ratelimit.All()

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("ratelimit.title"), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))

	if len(snaps) == 0 {
		fmt.Println(colorize("  "+i18n.T("ratelimit.empty"), ColorGray))
		fmt.Println()
		return
	}

	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Provider < snaps[j].Provider })
	for _, s := range snaps {
		fmt.Printf("  %s\n", colorize(s.Provider, ColorYellow))
		if s.Requests.Valid() {
			fmt.Printf("    %s\n", formatBucket(i18n.T("ratelimit.requests"), s.Requests))
		}
		if s.Tokens.Valid() {
			fmt.Printf("    %s\n", formatBucket(i18n.T("ratelimit.tokens"), s.Tokens))
		}
	}
	fmt.Println()
}

func formatBucket(label string, b ratelimit.Bucket) string {
	pct := int(b.UsagePct() * 100)
	color := ColorGreen
	if pct >= 90 {
		color = ColorRed
	} else if pct >= 70 {
		color = ColorYellow
	}
	return fmt.Sprintf("%-9s %d/%d  %s  %s",
		label,
		b.Remaining, b.Limit,
		colorize(i18n.T("ratelimit.used_pct", pct), color),
		colorize(i18n.T("ratelimit.resets_in", int(b.RemainingSeconds())), ColorGray),
	)
}
