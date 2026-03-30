package cli

import (
	"fmt"
	"strings"
	"time"
)

func (cli *ChatCLI) handleCostCommand() {
	if cli.costTracker == nil {
		fmt.Println(colorize("  Cost tracker não inicializado.", ColorYellow))
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
	fmt.Println(uiBox("💰", "SESSION COST", ColorCyan))
	p := uiPrefix(ColorCyan)

	duration := time.Since(ct.sessionStart).Truncate(time.Second)
	totalTokens := ct.promptTokens + ct.completionTokens

	fmt.Println(p + fmt.Sprintf("  %sProvider:%s    %s", ColorGray, ColorReset, provider))
	fmt.Println(p + fmt.Sprintf("  %sModel:%s       %s", ColorGray, ColorReset, model))
	fmt.Println(p + fmt.Sprintf("  %sDuração:%s     %s", ColorGray, ColorReset, duration))
	fmt.Println(p + fmt.Sprintf("  %sRequests:%s    %d", ColorGray, ColorReset, ct.totalRequests))
	fmt.Println(p)

	// Token breakdown with mini-bar
	maxToken := ct.promptTokens
	if ct.completionTokens > maxToken {
		maxToken = ct.completionTokens
	}

	promptBar := ""
	completionBar := ""
	if maxToken > 0 {
		promptBar = strings.Repeat("█", int(ct.promptTokens*20/maxToken))
		completionBar = strings.Repeat("█", int(ct.completionTokens*20/maxToken))
	}

	fmt.Println(p + colorize("  Tokens:", ColorCyan))
	fmt.Println(p + fmt.Sprintf("    Input:      %s%-8s%s %s%s%s",
		ColorBold, formatTokenCount64(ct.promptTokens), ColorReset,
		ColorGreen, promptBar, ColorReset))
	fmt.Println(p + fmt.Sprintf("    Output:     %s%-8s%s %s%s%s",
		ColorBold, formatTokenCount64(ct.completionTokens), ColorReset,
		ColorPurple, completionBar, ColorReset))
	fmt.Println(p + fmt.Sprintf("    Total:      %s%s%s",
		ColorBold, formatTokenCount64(totalTokens), ColorReset))
	fmt.Println(p)

	// Cost estimation
	inputCost, outputCost := getModelPricing(provider, model)
	if inputCost > 0 || outputCost > 0 {
		estInput := float64(ct.promptTokens) / 1_000_000 * inputCost
		estOutput := float64(ct.completionTokens) / 1_000_000 * outputCost
		estTotal := estInput + estOutput

		fmt.Println(p + colorize("  Custo Estimado:", ColorCyan))
		fmt.Println(p + fmt.Sprintf("    Input:      $%.4f  %s($%.2f/1M tokens)%s",
			estInput, ColorGray, inputCost, ColorReset))
		fmt.Println(p + fmt.Sprintf("    Output:     $%.4f  %s($%.2f/1M tokens)%s",
			estOutput, ColorGray, outputCost, ColorReset))
		fmt.Println(p + fmt.Sprintf("    %sTotal:      %s$%.4f%s",
			ColorBold, ColorLime, estTotal, ColorReset))
	} else {
		fmt.Println(p + colorize("  Pricing não disponível para este modelo.", ColorGray))
	}

	fmt.Println(p)
	fmt.Println(uiBoxEnd(ColorCyan))
	fmt.Println()
}
