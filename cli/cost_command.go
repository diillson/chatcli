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
)

func (cli *ChatCLI) handleCostCommand() {
	if cli.costTracker == nil {
		fmt.Println(colorize("  Cost tracker not initialized.", ColorYellow))
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
	fmt.Println(uiBox("$", "SESSION COST", ColorCyan))
	p := uiPrefix(ColorCyan)

	duration := time.Since(ct.sessionStart).Truncate(time.Second)
	totalTokens := ct.totalPromptTokens + ct.totalCompletionTokens

	fmt.Println(p + fmt.Sprintf("  %sProvider:%s    %s", ColorGray, ColorReset, provider))
	fmt.Println(p + fmt.Sprintf("  %sModel:%s       %s", ColorGray, ColorReset, model))
	fmt.Println(p + fmt.Sprintf("  %sDuration:%s    %s", ColorGray, ColorReset, duration))
	fmt.Println(p + fmt.Sprintf("  %sRequests:%s    %d", ColorGray, ColorReset, ct.totalRequests))

	// Data source indicator
	hasReal := false
	for _, rec := range ct.modelUsage {
		if rec.HasRealData {
			hasReal = true
			break
		}
	}
	if hasReal {
		fmt.Println(p + fmt.Sprintf("  %sSource:%s      %sAPI usage data%s", ColorGray, ColorReset, ColorGreen, ColorReset))
	} else {
		fmt.Println(p + fmt.Sprintf("  %sSource:%s      %scharacter estimate%s", ColorGray, ColorReset, ColorYellow, ColorReset))
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

	fmt.Println(p + colorize("  Tokens:", ColorCyan))
	fmt.Println(p + fmt.Sprintf("    Input:      %s%-8s%s %s%s%s",
		ColorBold, formatTokenCount64(ct.totalPromptTokens), ColorReset,
		ColorGreen, promptBar, ColorReset))
	fmt.Println(p + fmt.Sprintf("    Output:     %s%-8s%s %s%s%s",
		ColorBold, formatTokenCount64(ct.totalCompletionTokens), ColorReset,
		ColorPurple, completionBar, ColorReset))
	fmt.Println(p + fmt.Sprintf("    Total:      %s%s%s",
		ColorBold, formatTokenCount64(totalTokens), ColorReset))

	// Cache tokens (Anthropic only)
	if ct.totalCacheCreation > 0 || ct.totalCacheRead > 0 {
		fmt.Println(p)
		fmt.Println(p + colorize("  Cache Tokens:", ColorCyan))
		fmt.Println(p + fmt.Sprintf("    Created:    %s%s%s",
			ColorBold, formatTokenCount64(ct.totalCacheCreation), ColorReset))
		fmt.Println(p + fmt.Sprintf("    Read:       %s%s%s  %s(saves ~90%% input cost)%s",
			ColorBold, formatTokenCount64(ct.totalCacheRead), ColorReset,
			ColorGray, ColorReset))
	}
	fmt.Println(p)

	// Cost estimation
	if ct.totalCostUSD > 0 {
		fmt.Println(p + colorize("  Cost:", ColorCyan))

		// Show per-model cost breakdown
		for _, rec := range ct.modelUsage {
			if rec.TotalCostUSD <= 0 {
				continue
			}
			fmt.Println(p + fmt.Sprintf("    %s/%s:", rec.Provider, rec.Model))
			fmt.Println(p + fmt.Sprintf("      Input:    $%.4f", rec.InputCostUSD))
			fmt.Println(p + fmt.Sprintf("      Output:   $%.4f", rec.OutputCostUSD))
			if rec.CacheCostUSD > 0 {
				fmt.Println(p + fmt.Sprintf("      Cache:    $%.4f", rec.CacheCostUSD))
			}
		}

		fmt.Println(p)
		fmt.Println(p + fmt.Sprintf("    %sTotal:      %s$%.4f%s",
			ColorBold, ColorLime, ct.totalCostUSD, ColorReset))
	} else {
		fmt.Println(p + colorize("  Pricing not available for this model.", ColorGray))
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
