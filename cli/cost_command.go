/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/utils"
)

// handleCostCommand dispatches /cost and its subcommands. Parsing is
// deliberately lenient ("last", "--last" and "-l" are all accepted): a
// mistyped flag should degrade to help, never to a silent no-op.
func (cli *ChatCLI) handleCostCommand(args string) {
	if cli.costTracker == nil {
		fmt.Println(colorize("  "+i18n.T("cost.cmd.not_initialized"), ColorYellow))
		return
	}
	// Keep the persisted snapshot labeled with the live session identity.
	cli.costTracker.SetSessionName(cli.currentSessionName)

	fields := strings.Fields(strings.TrimSpace(args))
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(strings.TrimLeft(fields[0], "-"))
	}

	switch sub {
	case "":
		cli.renderCostSummary()
	case "reset":
		cli.handleCostReset()
	case "last", "l", "prev", "previous":
		cli.handleCostLast()
	case "sessions", "history", "list":
		cli.handleCostSessions()
	case "export":
		path := ""
		if len(fields) > 1 {
			path = fields[1]
		}
		cli.handleCostExport(path)
	default:
		fmt.Println(colorize("  "+i18n.T("cost.cmd.help"), ColorGray))
		cli.renderCostSummary()
	}
}

// handleCostReset closes the current accounting period (persisting it) and
// starts a fresh one.
func (cli *ChatCLI) handleCostReset() {
	previousID := cli.costTracker.CurrentSessionID()
	cli.costTracker.Reset()
	fmt.Println(colorize("  "+i18n.T("cost.cmd.reset_done", previousID), ColorGreen))
}

// handleCostLast renders the most recent persisted snapshot that is not the
// live session — the spend of the previous CLI run (or period, after a
// /cost reset).
func (cli *ChatCLI) handleCostLast() {
	snapshots, err := ListCostSnapshots(0)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("cost.cmd.snapshot_failed", err), ColorYellow))
		return
	}
	currentID := cli.costTracker.CurrentSessionID()
	for _, snap := range snapshots {
		if snap.SessionID == currentID {
			continue
		}
		cli.renderCostSnapshot(snap, i18n.T("cost.cmd.last_title"))
		return
	}
	fmt.Println(colorize("  "+i18n.T("cost.cmd.last_none"), ColorGray))
}

// handleCostSessions lists recent persisted snapshots, most recent first.
func (cli *ChatCLI) handleCostSessions() {
	snapshots, err := ListCostSnapshots(10)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("cost.cmd.snapshot_failed", err), ColorYellow))
		return
	}
	if len(snapshots) == 0 {
		fmt.Println(colorize("  "+i18n.T("cost.cmd.sessions_none"), ColorGray))
		return
	}

	fmt.Println()
	fmt.Println(uiBox("$", i18n.T("cost.cmd.sessions_title"), ColorCyan))
	p := uiPrefix(ColorCyan)
	currentID := cli.costTracker.CurrentSessionID()
	for _, snap := range snapshots {
		label := snap.SessionID
		if snap.SessionName != "" {
			label += " · " + snap.SessionName
		}
		if snap.SessionID == currentID {
			label += " " + i18n.T("cost.cmd.sessions_current")
		}
		fmt.Println(p + "  " + ColorBold + label + ColorReset)
		fmt.Println(p + "    " + colorize(
			i18n.T("cost.cmd.sessions_row",
				snap.LastUpdate.Format("2006-01-02 15:04"),
				snap.TotalRequests,
				formatTokenCount64(snap.TotalTokens),
				fmt.Sprintf("$%.4f", snap.TotalCostUSD)),
			ColorGray))
	}
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}

// handleCostExport writes the current session snapshot as JSON — to the
// given path, or to the cost store with an -export suffix when omitted.
func (cli *ChatCLI) handleCostExport(path string) {
	snap := cli.costTracker.Snapshot()
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("cost.cmd.export_failed", err), ColorYellow))
		return
	}
	if path == "" {
		path = filepath.Join(costStoreDir(), snap.SessionID+"-export.json")
	} else if expanded, expErr := utils.ExpandPath(path); expErr == nil {
		path = expanded
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Println(colorize("  "+i18n.T("cost.cmd.export_failed", err), ColorYellow))
		return
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		fmt.Println(colorize("  "+i18n.T("cost.cmd.export_failed", err), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("cost.cmd.export_done", path), ColorGreen))
}

// renderCostSummary paints the live session box.
func (cli *ChatCLI) renderCostSummary() {
	ct := cli.costTracker
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	provider := cli.Provider
	model := ""
	if cli.Client != nil {
		model = cli.Client.GetModelName()
	}

	fmt.Println()
	fmt.Println(uiBox("$", i18n.T("cost.cmd.box_title"), ColorCyan))
	p := uiPrefix(ColorCyan)

	duration := time.Since(ct.sessionStart).Truncate(time.Second)
	totalTokens := ct.totalPromptTokens + ct.totalCompletionTokens

	// Aligned key/value rows: the label column is measured from the
	// TRANSLATED labels of each group (kit.PadRight is visible-width
	// aware), replacing the literal spaces that were hand-tuned to the
	// English label lengths and drifted in pt-BR.
	row := func(labelW int, label, value string) {
		fmt.Println(p + "  " + colorize(kit.PadRight(label+":", labelW+1), ColorGray) + "  " + value)
	}
	groupWidth := func(labels ...string) int {
		w := 0
		for _, l := range labels {
			if lw := kit.VisibleLen(l); lw > w {
				w = lw
			}
		}
		return w
	}

	topLabels := []string{
		i18n.T("cost.cmd.provider"), i18n.T("cost.cmd.model"),
		i18n.T("cost.cmd.duration"), i18n.T("cost.cmd.requests"),
		i18n.T("cost.cmd.source"),
	}
	topW := groupWidth(topLabels...)
	row(topW, i18n.T("cost.cmd.provider"), provider)
	row(topW, i18n.T("cost.cmd.model"), model)
	row(topW, i18n.T("cost.cmd.duration"), duration.String())
	row(topW, i18n.T("cost.cmd.requests"), fmt.Sprintf("%d", ct.totalRequests))

	// Data source indicator
	hasReal := false
	for _, rec := range ct.modelUsage {
		if rec.HasRealData {
			hasReal = true
			break
		}
	}
	if hasReal {
		row(topW, i18n.T("cost.cmd.source"), colorize(i18n.T("cost.cmd.source_api"), ColorGreen))
	} else {
		row(topW, i18n.T("cost.cmd.source"), colorize(i18n.T("cost.cmd.source_estimate"), ColorYellow))
	}
	fmt.Println(p)

	// Token breakdown with mini-bar
	maxToken := ct.totalPromptTokens
	if ct.totalCompletionTokens > maxToken {
		maxToken = ct.totalCompletionTokens
	}

	promptBar := ""
	completionBar := ""
	if maxToken > 0 {
		promptBar = strings.Repeat("█", int(ct.totalPromptTokens*20/maxToken))
		completionBar = strings.Repeat("█", int(ct.totalCompletionTokens*20/maxToken))
	}

	tokenLabels := []string{i18n.T("cost.cmd.input"), i18n.T("cost.cmd.output"), i18n.T("cost.cmd.total")}
	if ct.totalReasoning > 0 {
		tokenLabels = append(tokenLabels, i18n.T("cost.cmd.reasoning"))
	}
	tokenW := groupWidth(tokenLabels...)
	tokenRow := func(label, count, bar string) {
		line := "    " + kit.PadRight(label, tokenW+2) + ColorBold + kit.PadRight(count, 8) + ColorReset
		if bar != "" {
			line += " " + bar
		}
		fmt.Println(p + line)
	}
	fmt.Println(p + colorize("  "+i18n.T("cost.cmd.tokens_label"), ColorCyan))
	tokenRow(i18n.T("cost.cmd.input"), formatTokenCount64(ct.totalPromptTokens), ColorGreen+promptBar+ColorReset)
	tokenRow(i18n.T("cost.cmd.output"), formatTokenCount64(ct.totalCompletionTokens), ColorPurple+completionBar+ColorReset)
	tokenRow(i18n.T("cost.cmd.total"), formatTokenCount64(totalTokens), "")
	if ct.totalReasoning > 0 {
		// Informational: reasoning tokens are already inside Output.
		tokenRow(i18n.T("cost.cmd.reasoning"), formatTokenCount64(ct.totalReasoning),
			colorize(i18n.T("cost.cmd.reasoning_note"), ColorGray))
	}

	// Cache tokens
	if ct.totalCacheCreation > 0 || ct.totalCacheRead > 0 {
		cacheW := groupWidth(i18n.T("cost.cmd.cache_created"), i18n.T("cost.cmd.cache_read"))
		fmt.Println(p)
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.cache_tokens_label"), ColorCyan))
		fmt.Println(p + "    " + kit.PadRight(i18n.T("cost.cmd.cache_created"), cacheW+2) +
			ColorBold + formatTokenCount64(ct.totalCacheCreation) + ColorReset)
		savings := ""
		if saved := cacheSavingsUSDLocked(ct); saved > 0.00005 {
			savings = "  " + colorize(i18n.T("cost.cmd.cache_saved_usd", fmt.Sprintf("$%.4f", saved)), ColorGray)
		}
		fmt.Println(p + "    " + kit.PadRight(i18n.T("cost.cmd.cache_read"), cacheW+2) +
			ColorBold + formatTokenCount64(ct.totalCacheRead) + ColorReset + savings)
	}
	// Explicit cache resources: storage bought for the granted lifetimes.
	if ct.cacheResources > 0 {
		fmt.Println(p + "    " + colorize(i18n.T("cost.cmd.cache_storage",
			ct.cacheResources, fmt.Sprintf("$%.4f", ct.cacheStorageUSD)), ColorGray))
	}
	if ct.compactions > 0 {
		fmt.Println(p + "    " + colorize(i18n.T("cost.cmd.compactions",
			ct.compactions, ct.compactionsLevel3, fmt.Sprintf("$%.4f", ct.compactionCostUSD)), ColorGray))
	}
	// Session prompt-cache telemetry: hit share, misses, rebuilds ChatCLI
	// itself caused (compaction), and whether the prefix is still warm.
	if stats := ct.cacheStatsLocked(); stats.Reported() {
		fmt.Println(p)
		state := i18n.T("cost.cmd.cache_cold", stats.TTL, formatIdle(time.Since(stats.LastActivity)))
		if stats.Warm {
			state = i18n.T("cost.cmd.cache_warm", stats.TTL, formatIdle(time.Since(stats.LastActivity)))
		}
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.cache_stats",
			stats.Requests, fmt.Sprintf("%.0f%%", stats.HitPct), stats.Misses, stats.Rebuilds), ColorCyan) +
			" " + colorize(state, ColorGray))
	}
	fmt.Println(p)

	// Cost estimation
	if ct.totalCostUSD > 0 {
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.cost_label"), ColorCyan))

		// Show per-model cost breakdown, stable order (largest spend first).
		costW := groupWidth(i18n.T("cost.cmd.input_cost"), i18n.T("cost.cmd.output_cost"), i18n.T("cost.cmd.cache_cost"))
		for _, rec := range sortedRecords(ct.modelUsage) {
			if rec.TotalCostUSD <= 0 {
				continue
			}
			fmt.Println(p + fmt.Sprintf("    %s/%s: %s", rec.Provider, rec.Model, recordSourceTag(rec)))
			tableCost := rec.InputCostUSD + rec.OutputCostUSD + rec.CacheCostUSD
			if tableCost > 0 || rec.ProviderCostUSD == 0 {
				// Table-priced share (all of it when no provider-billed part).
				fmt.Println(p + "      " + kit.PadRight(i18n.T("cost.cmd.input_cost"), costW+2) + fmt.Sprintf("$%.4f", rec.InputCostUSD))
				fmt.Println(p + "      " + kit.PadRight(i18n.T("cost.cmd.output_cost"), costW+2) + fmt.Sprintf("$%.4f", rec.OutputCostUSD))
				if rec.CacheCostUSD > 0 {
					fmt.Println(p + "      " + kit.PadRight(i18n.T("cost.cmd.cache_cost"), costW+2) + fmt.Sprintf("$%.4f", rec.CacheCostUSD))
				}
			}
			if rec.ProviderCostUSD > 0 {
				// Share the provider billed directly (usage.cost) — shown as
				// its own line so a mixed key surfaces both parts.
				fmt.Println(p + "      " + kit.PadRight(i18n.T("cost.cmd.total"), costW+2) + fmt.Sprintf("$%.4f", rec.ProviderCostUSD) +
					"  " + colorize(i18n.T("cost.cmd.tag_provider_billed"), ColorGray))
			}
		}

		fmt.Println(p)
		fmt.Println(p + "    " + ColorBold + kit.PadRight(i18n.T("cost.cmd.total"), costW+2) + ColorLime + fmt.Sprintf("$%.4f", ct.totalCostUSD) + ColorReset)
	} else {
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.pricing_unavailable"), ColorGray))
	}

	// Models whose price ChatCLI does not know: their spend is NOT in the
	// total. Say so instead of silently under-reporting.
	if unpriced := unpricedModelsLocked(ct); len(unpriced) > 0 {
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.pricing_unknown_models", strings.Join(unpriced, ", ")), ColorYellow))
	}

	printDailyBudgetLine(p, ct)

	// Budget status
	if msg := ct.budgetMessageLocked(); msg != "" {
		fmt.Println(p)
		if ct.budgetLevelLocked() == BudgetExceeded {
			fmt.Println(p + colorize("  "+msg, ColorRed))
		} else {
			fmt.Println(p + colorize("  "+msg, ColorYellow))
		}
	}

	fmt.Println(p)
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}

// renderCostSnapshot paints a persisted snapshot (/cost last).
func (cli *ChatCLI) renderCostSnapshot(snap *SessionCostData, title string) {
	fmt.Println()
	fmt.Println(uiBox("$", title, ColorCyan))
	p := uiPrefix(ColorCyan)

	label := snap.SessionID
	if snap.SessionName != "" {
		label += " · " + snap.SessionName
	}
	fmt.Println(p + "  " + ColorBold + label + ColorReset)
	fmt.Println(p + "  " + colorize(
		i18n.T("cost.cmd.sessions_row",
			snap.LastUpdate.Format("2006-01-02 15:04"),
			snap.TotalRequests,
			formatTokenCount64(snap.TotalTokens),
			fmt.Sprintf("$%.4f", snap.TotalCostUSD)),
		ColorGray))

	if len(snap.ModelUsage) > 0 {
		fmt.Println(p)
		for _, rec := range sortedRecords(snap.ModelUsage) {
			fmt.Println(p + fmt.Sprintf("    %s/%s: %s", rec.Provider, rec.Model, recordSourceTag(rec)))
			fmt.Println(p + "      " + colorize(
				i18n.T("cost.cmd.snapshot_model_row",
					formatTokenCount64(rec.TotalTokens),
					rec.Requests,
					fmt.Sprintf("$%.4f", rec.TotalCostUSD)),
				ColorGray))
		}
	}
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}

// sortedRecords returns the usage records ordered by descending spend, then
// tokens, then name — a stable order for rendering (map iteration is not).
func sortedRecords(usage map[string]*ModelUsageRecord) []*ModelUsageRecord {
	out := make([]*ModelUsageRecord, 0, len(usage))
	for _, rec := range usage {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalCostUSD != out[j].TotalCostUSD {
			return out[i].TotalCostUSD > out[j].TotalCostUSD
		}
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		return out[i].Provider+out[i].Model < out[j].Provider+out[j].Model
	})
	return out
}

// recordSourceTag renders the per-model data-source tag: real API counts or
// character estimate. Per model — a session can mix both.
func recordSourceTag(rec *ModelUsageRecord) string {
	if rec.HasRealData {
		return colorize(i18n.T("cost.cmd.tag_api"), ColorGreen)
	}
	return colorize(i18n.T("cost.cmd.tag_estimate"), ColorYellow)
}

// unpricedModelsLocked lists models that carry tokens but matched no pricing
// table entry (and reported no provider cost). Caller holds ct.mu.
func unpricedModelsLocked(ct *CostTracker) []string {
	var out []string
	for _, rec := range ct.modelUsage {
		if !rec.PricingKnown && rec.TotalTokens > 0 && rec.ProviderCostUSD == 0 {
			out = append(out, rec.Provider+"/"+rec.Model)
		}
	}
	sort.Strings(out)
	return out
}

// cacheSavingsUSDLocked estimates how much the session saved because cache
// reads were billed at the discounted rate instead of the full input price.
// Caller holds ct.mu.
func cacheSavingsUSDLocked(ct *CostTracker) float64 {
	saved := 0.0
	for _, rec := range ct.modelUsage {
		if rec.CacheReadTokens == 0 {
			continue
		}
		inputCost, _, known := lookupModelPricing(rec.Provider, rec.Model)
		if !known || inputCost <= 0 {
			continue
		}
		_, readCost := getCachePricing(rec.Provider, rec.Model)
		if readCost <= 0 || readCost >= inputCost {
			continue
		}
		saved += float64(rec.CacheReadTokens) / 1_000_000 * (inputCost - readCost)
	}
	return saved
}
