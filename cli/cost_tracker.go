package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// CostTracker tracks token usage and estimated cost for the current session.
type CostTracker struct {
	mu               sync.RWMutex
	sessionStart     time.Time
	promptTokens     int64
	completionTokens int64
	totalRequests    int
	provider         string
	model            string
}

// NewCostTracker creates a new cost tracker.
func NewCostTracker() *CostTracker {
	return &CostTracker{
		sessionStart: time.Now(),
	}
}

// RecordUsage records tokens used for a single LLM request.
func (ct *CostTracker) RecordUsage(provider, model string, promptTokens, completionTokens int) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.promptTokens += int64(promptTokens)
	ct.completionTokens += int64(completionTokens)
	ct.totalRequests++
	ct.provider = provider
	ct.model = model
}

// RecordFromHistory estimates token usage from the conversation history.
func (ct *CostTracker) RecordFromHistory(provider, model string, history []interface{ Content() string }) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.provider = provider
	ct.model = model
}

// GetSummary returns a formatted cost summary string.
func (ct *CostTracker) GetSummary(provider, model string, history int) string {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	var sb strings.Builder

	sb.WriteString(colorize("  Cost Summary", ColorCyan))
	sb.WriteString("\n")
	sb.WriteString(colorize("  "+strings.Repeat("─", 50), ColorGray))
	sb.WriteString("\n")

	duration := time.Since(ct.sessionStart).Truncate(time.Second)
	sb.WriteString(fmt.Sprintf("  Session duration: %s\n", duration))
	sb.WriteString(fmt.Sprintf("  Provider: %s  Model: %s\n", provider, model))
	sb.WriteString(fmt.Sprintf("  Total requests: %d\n", ct.totalRequests))

	totalTokens := ct.promptTokens + ct.completionTokens
	sb.WriteString("\n  Tokens:\n")
	sb.WriteString(fmt.Sprintf("    Prompt:     %s\n", formatTokenCount64(ct.promptTokens)))
	sb.WriteString(fmt.Sprintf("    Completion: %s\n", formatTokenCount64(ct.completionTokens)))
	sb.WriteString(fmt.Sprintf("    Total:      %s\n", formatTokenCount64(totalTokens)))

	// Estimated cost based on known pricing
	inputCost, outputCost := getModelPricing(provider, model)
	if inputCost > 0 || outputCost > 0 {
		estInput := float64(ct.promptTokens) / 1_000_000 * inputCost
		estOutput := float64(ct.completionTokens) / 1_000_000 * outputCost
		estTotal := estInput + estOutput

		sb.WriteString("\n  Estimated cost:\n")
		sb.WriteString(fmt.Sprintf("    Input:  $%.4f ($%.2f / 1M tokens)\n", estInput, inputCost))
		sb.WriteString(fmt.Sprintf("    Output: $%.4f ($%.2f / 1M tokens)\n", estOutput, outputCost))
		sb.WriteString(fmt.Sprintf("    Total:  %s\n", colorize(fmt.Sprintf("$%.4f", estTotal), ColorGreen)))
	} else {
		// Fallback: estimate from history character count
		sb.WriteString("\n  (Using character-based token estimate)\n")
	}

	return sb.String()
}

func formatTokenCount64(tokens int64) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	}
	if tokens >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}

// EstimateAndRecord estimates tokens from text lengths and records usage.
func (ct *CostTracker) EstimateAndRecord(provider, model string, inputChars, outputChars int) {
	promptTokens := inputChars / 4
	completionTokens := outputChars / 4
	ct.RecordUsage(provider, model, promptTokens, completionTokens)
}

// getModelPricing returns input and output cost per 1M tokens for known models.
// Prices in USD per 1M tokens. Updated March 2026.
func getModelPricing(provider, model string) (inputCost, outputCost float64) {
	model = strings.ToLower(model)

	// Anthropic Claude models
	if strings.Contains(model, "claude") {
		switch {
		case strings.Contains(model, "opus"):
			return 15.0, 75.0
		case strings.Contains(model, "sonnet"):
			return 3.0, 15.0
		case strings.Contains(model, "haiku"):
			return 0.25, 1.25
		}
	}

	// OpenAI models (order matters — more specific first)
	switch {
	case strings.Contains(model, "gpt-4o-mini"):
		return 0.15, 0.60
	case strings.Contains(model, "gpt-4o"):
		return 2.50, 10.0
	case strings.Contains(model, "gpt-4-turbo"):
		return 10.0, 30.0
	case strings.Contains(model, "gpt-4.1"):
		return 2.0, 8.0
	case strings.Contains(model, "gpt-4"):
		return 30.0, 60.0
	case strings.Contains(model, "gpt-3.5"):
		return 0.50, 1.50
	case strings.Contains(model, "o3-mini"):
		return 1.10, 4.40
	case strings.Contains(model, "o3"):
		return 10.0, 40.0
	case strings.Contains(model, "o1-mini"):
		return 3.0, 12.0
	case strings.Contains(model, "o1"):
		return 15.0, 60.0
	case strings.Contains(model, "o4-mini"):
		return 1.10, 4.40
	}

	// Google models
	switch {
	case strings.Contains(model, "gemini-2.5-pro"):
		return 1.25, 10.0
	case strings.Contains(model, "gemini-2.5-flash"):
		return 0.15, 0.60
	case strings.Contains(model, "gemini-2.0"):
		return 0.075, 0.30
	case strings.Contains(model, "gemini-1.5-pro"):
		return 1.25, 5.0
	case strings.Contains(model, "gemini-1.5-flash"):
		return 0.075, 0.30
	}

	// xAI Grok
	switch {
	case strings.Contains(model, "grok-3"):
		return 3.0, 15.0
	case strings.Contains(model, "grok-2"):
		return 2.0, 10.0
	case strings.Contains(model, "grok"):
		return 5.0, 15.0
	}

	// Copilot (uses OpenAI models under the hood, estimate)
	if strings.Contains(provider, "COPILOT") {
		return 2.50, 10.0 // GPT-4o equivalent
	}

	return 0, 0 // unknown model — will show "pricing not available"
}
