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
	"github.com/diillson/chatcli/ui/kit"
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
		promptBar = strings.Repeat("\u2588", int(ct.totalPromptTokens*20/maxToken))
		completionBar = strings.Repeat("\u2588", int(ct.totalCompletionTokens*20/maxToken))
	}

	tokenW := groupWidth(i18n.T("cost.cmd.input"), i18n.T("cost.cmd.output"), i18n.T("cost.cmd.total"))
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

	// Cache tokens (Anthropic only)
	if ct.totalCacheCreation > 0 || ct.totalCacheRead > 0 {
		cacheW := groupWidth(i18n.T("cost.cmd.cache_created"), i18n.T("cost.cmd.cache_read"))
		fmt.Println(p)
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.cache_tokens_label"), ColorCyan))
		fmt.Println(p + "    " + kit.PadRight(i18n.T("cost.cmd.cache_created"), cacheW+2) +
			ColorBold + formatTokenCount64(ct.totalCacheCreation) + ColorReset)
		fmt.Println(p + "    " + kit.PadRight(i18n.T("cost.cmd.cache_read"), cacheW+2) +
			ColorBold + formatTokenCount64(ct.totalCacheRead) + ColorReset + "  " +
			colorize(i18n.T("cost.cmd.cache_savings"), ColorGray))
	}
	fmt.Println(p)

	// Cost estimation
	if ct.totalCostUSD > 0 {
		fmt.Println(p + colorize("  "+i18n.T("cost.cmd.cost_label"), ColorCyan))

		// Show per-model cost breakdown
		costW := groupWidth(i18n.T("cost.cmd.input_cost"), i18n.T("cost.cmd.output_cost"), i18n.T("cost.cmd.cache_cost"))
		for _, rec := range ct.modelUsage {
			if rec.TotalCostUSD <= 0 {
				continue
			}
			fmt.Println(p + fmt.Sprintf("    %s/%s:", rec.Provider, rec.Model))
			fmt.Println(p + "      " + kit.PadRight(i18n.T("cost.cmd.input_cost"), costW+2) + fmt.Sprintf("$%.4f", rec.InputCostUSD))
			fmt.Println(p + "      " + kit.PadRight(i18n.T("cost.cmd.output_cost"), costW+2) + fmt.Sprintf("$%.4f", rec.OutputCostUSD))
			if rec.CacheCostUSD > 0 {
				fmt.Println(p + "      " + kit.PadRight(i18n.T("cost.cmd.cache_cost"), costW+2) + fmt.Sprintf("$%.4f", rec.CacheCostUSD))
			}
		}

		fmt.Println(p)
		fmt.Println(p + "    " + ColorBold + kit.PadRight(i18n.T("cost.cmd.total"), costW+2) + ColorLime + fmt.Sprintf("$%.4f", ct.totalCostUSD) + ColorReset)
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
