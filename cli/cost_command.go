/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
)

func (cli *ChatCLI) handleCostCommand() {
	if cli.costTracker == nil {
		fmt.Println(colorize("  "+i18n.T("cost.cmd.not_initialized"), ColorYellow))
		return
	}

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

	fmt.Println(p + fmt.Sprintf("  %s%s:%s    %s", ColorGray, i18n.T("cost.cmd.provider"), ColorReset, provider))
	fmt.Println(p + fmt.Sprintf("  %s%s:%s       %s", ColorGray, i18n.T("cost.cmd.model"), ColorReset, model))
	fmt.Println(p + fmt.Sprintf("  %s%s:%s    %s", ColorGray, i18n.T("cost.cmd.duration"), ColorReset, duration))
	fmt.Println(p + fmt.Sprintf("  %s%s:%s    %d", ColorGray, i18n.T("cost.cmd.requests"), ColorReset, ct.totalRequests))

	// Data source indicator
	hasReal := false
	for _, rec := range ct.modelUsage {
		if rec.HasRealData {
			hasReal = true
			break
		}
	}
	if hasReal {
		fmt.Println(p + fmt.Sprintf("  %s%s:%s      %s%s%s", ColorGray, i18n.T("cost.cmd.source"), ColorReset, ColorGreen, i18n.T("cost.cmd.source_api"), ColorReset))
	} else {
		fmt.Println(p + fmt.Sprintf("  %s%s:%s      %s%s%s", ColorGray, i18n.T("cost.cmd.source"), ColorReset, ColorYellow, i18n.T("cost.cmd.source_estimate"), ColorReset))
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
		promptBar = strings.Repeat("\u2588", int(ct.totalPromptTokens*20/maxToken))
		completionBar = strings.Repeat("\u2588", int(ct.totalCompletionTokens*20/maxToken))
	}

	fmt.Println(p + colorize("  "+i18n.T("cost.cmd.tokens_label"), ColorCyan))
	fmt.Println(p + fmt.Sprintf("    %s      %s%-8s%s %s%s%s",
		i18n.T("cost.cmd.input"),
		ColorBold, formatTokenCount64(ct.totalPromptTokens), ColorReset,
		ColorGreen, promptBar, ColorReset))
	fmt.Println(p + fmt.Sprintf("    %s     %s%-8s%s %s%s%s",
		i18n.T("cost.cmd.output"),
		ColorBold, formatTokenCount64(ct.totalCompletionTokens), ColorReset,
		ColorPurple, completionBar, ColorReset))
	fmt.Println(p + fmt.Sprintf("    %s      %s%s%s",
		i18n.T("cost.cmd.total"),
		ColorBold, formatTokenCount64(totalTokens), ColorReset))

	// Cache tokens (Anthropic only)
	if ct.totalCacheCreation > 0 || ct.totalCacheRead > 0 {
		fmt.Println(p)
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.cache_tokens_label"), ColorCyan))
		fmt.Println(p + fmt.Sprintf("    %s    %s%s%s",
			i18n.T("cost.cmd.cache_created"),
			ColorBold, formatTokenCount64(ct.totalCacheCreation), ColorReset))
		fmt.Println(p + fmt.Sprintf("    %s       %s%s%s  %s%s%s",
			i18n.T("cost.cmd.cache_read"),
			ColorBold, formatTokenCount64(ct.totalCacheRead), ColorReset,
			ColorGray, i18n.T("cost.cmd.cache_savings"), ColorReset))
	}
	fmt.Println(p)

	// Cost estimation
	if ct.totalCostUSD > 0 {
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.cost_label"), ColorCyan))

		// Show per-model cost breakdown
		for _, rec := range ct.modelUsage {
			if rec.TotalCostUSD <= 0 {
				continue
			}
			fmt.Println(p + fmt.Sprintf("    %s/%s:", rec.Provider, rec.Model))
			fmt.Println(p + fmt.Sprintf("      %s    $%.4f", i18n.T("cost.cmd.input_cost"), rec.InputCostUSD))
			fmt.Println(p + fmt.Sprintf("      %s   $%.4f", i18n.T("cost.cmd.output_cost"), rec.OutputCostUSD))
			if rec.CacheCostUSD > 0 {
				fmt.Println(p + fmt.Sprintf("      %s    $%.4f", i18n.T("cost.cmd.cache_cost"), rec.CacheCostUSD))
			}
		}

		fmt.Println(p)
		fmt.Println(p + fmt.Sprintf("    %s%s      %s$%.4f%s",
			ColorBold, i18n.T("cost.cmd.total"), ColorLime, ct.totalCostUSD, ColorReset))
	} else {
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.pricing_unavailable"), ColorGray))
	}

	// Budget status
	if msg := ct.budgetMessageLocked(); msg != "" {
		fmt.Println(p)
		if ct.totalCostUSD >= ct.budgetLimitUSD {
			fmt.Println(p + colorize("  "+msg, ColorRed))
		} else {
			fmt.Println(p + colorize("  "+msg, ColorYellow))
		}
	}

	fmt.Println(p)
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}
