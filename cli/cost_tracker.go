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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/models"
)

// BudgetLevel indicates how close the session is to its spending limit.
type BudgetLevel int

const (
	// BudgetOK indicates spending is within normal limits.
	BudgetOK BudgetLevel = iota
	// BudgetWarning indicates spending has reached the warning threshold.
	BudgetWarning
	// BudgetExceeded indicates spending has exceeded the configured limit.
	BudgetExceeded
)

// ModelUsageRecord tracks cumulative token usage and cost for a single model.
type ModelUsageRecord struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`

	// Core token counts
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	// Anthropic cache tokens
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int64 `json:"cache_read_tokens,omitempty"`

	// Tracking
	Requests    int  `json:"requests"`
	HasRealData bool `json:"has_real_data"` // true if at least one call returned API usage

	// Computed cost (in USD)
	InputCostUSD  float64 `json:"input_cost_usd"`
	OutputCostUSD float64 `json:"output_cost_usd"`
	CacheCostUSD  float64 `json:"cache_cost_usd"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
}

// SessionCostData is the serializable snapshot of a cost tracking session.
type SessionCostData struct {
	SessionID     string                       `json:"session_id"`
	StartTime     time.Time                    `json:"start_time"`
	LastUpdate    time.Time                    `json:"last_update"`
	ModelUsage    map[string]*ModelUsageRecord `json:"model_usage"` // key: "provider:model"
	TotalCostUSD  float64                      `json:"total_cost_usd"`
	TotalRequests int                          `json:"total_requests"`
}

// CostTracker tracks token usage and estimated cost for the current session,
// with per-model granularity, real API usage data support, cache token pricing,
// session persistence, and configurable budget enforcement.
type CostTracker struct {
	mu sync.RWMutex

	sessionID    string
	sessionStart time.Time
	lastUpdate   time.Time

	// Per-model usage: key is "provider:model"
	modelUsage map[string]*ModelUsageRecord

	// Aggregates (computed from modelUsage)
	totalPromptTokens     int64
	totalCompletionTokens int64
	totalCacheCreation    int64
	totalCacheRead        int64
	totalRequests         int
	totalCostUSD          float64

	// Budget enforcement
	budgetLimitUSD   float64 // 0 = no limit
	budgetWarningPct float64 // fraction (0.8 = 80%)

	// For backward compat display
	lastProvider string
	lastModel    string
}

// NewCostTracker creates a new cost tracker with optional budget limit.
func NewCostTracker() *CostTracker {
	budgetLimit := 0.0
	if v := os.Getenv("CHATCLI_SESSION_BUDGET_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			budgetLimit = f
		}
	}

	warningPct := 0.80
	if v := os.Getenv("CHATCLI_BUDGET_WARNING_PCT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			warningPct = f
		}
	}

	return &CostTracker{
		sessionID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		sessionStart:     time.Now(),
		lastUpdate:       time.Now(),
		modelUsage:       make(map[string]*ModelUsageRecord),
		budgetLimitUSD:   budgetLimit,
		budgetWarningPct: warningPct,
	}
}

// modelKey returns the map key for a provider+model pair.
func modelKey(provider, model string) string {
	return strings.ToLower(provider) + ":" + strings.ToLower(model)
}

// RecordRealUsage records actual token usage from an API response.
// This is the preferred path — provides accurate cost tracking.
func (ct *CostTracker) RecordRealUsage(provider, model string, usage *models.UsageInfo) {
	if usage == nil {
		return
	}
	ct.mu.Lock()
	defer ct.mu.Unlock()

	key := modelKey(provider, model)
	rec := ct.getOrCreateRecord(key, provider, model)

	rec.PromptTokens += int64(usage.PromptTokens)
	rec.CompletionTokens += int64(usage.CompletionTokens)
	rec.TotalTokens += int64(usage.TotalTokens)
	rec.CacheCreationTokens += int64(usage.CacheCreationInputTokens)
	rec.CacheReadTokens += int64(usage.CacheReadInputTokens)
	rec.Requests++
	if usage.IsReal {
		rec.HasRealData = true
	}

	// Compute cost for this increment
	ct.recomputeCost(rec)
	ct.recomputeAggregates()

	ct.lastProvider = provider
	ct.lastModel = model
	ct.lastUpdate = time.Now()
}

// RecordUsage records tokens used for a single LLM request (legacy path).
func (ct *CostTracker) RecordUsage(provider, model string, promptTokens, completionTokens int) {
	ct.RecordRealUsage(provider, model, &models.UsageInfo{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		IsReal:           false,
	})
}

// EstimateAndRecord estimates tokens from text lengths and records usage.
func (ct *CostTracker) EstimateAndRecord(provider, model string, inputChars, outputChars int) {
	ct.RecordRealUsage(provider, model, models.EstimateFromChars(inputChars, outputChars))
}

// RecordFromHistory is kept for backward compatibility.
func (ct *CostTracker) RecordFromHistory(provider, model string, history []interface{ Content() string }) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.lastProvider = provider
	ct.lastModel = model
}

// CheckBudget returns the current budget level.
func (ct *CostTracker) CheckBudget() BudgetLevel {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	if ct.budgetLimitUSD <= 0 {
		return BudgetOK
	}
	if ct.totalCostUSD >= ct.budgetLimitUSD {
		return BudgetExceeded
	}
	if ct.totalCostUSD >= ct.budgetLimitUSD*ct.budgetWarningPct {
		return BudgetWarning
	}
	return BudgetOK
}

// BudgetMessage returns a human-readable budget status message.
// Returns empty string if no budget is configured or if spending is within limits.
func (ct *CostTracker) BudgetMessage() string {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	if ct.budgetLimitUSD <= 0 {
		return ""
	}

	pct := ct.totalCostUSD / ct.budgetLimitUSD * 100
	if ct.totalCostUSD >= ct.budgetLimitUSD {
		return fmt.Sprintf("BUDGET EXCEEDED: $%.4f / $%.2f (%.0f%%)", ct.totalCostUSD, ct.budgetLimitUSD, pct)
	}
	if ct.totalCostUSD >= ct.budgetLimitUSD*ct.budgetWarningPct {
		return fmt.Sprintf("Budget warning: $%.4f / $%.2f (%.0f%%)", ct.totalCostUSD, ct.budgetLimitUSD, pct)
	}
	return ""
}

// TotalCost returns the total estimated cost in USD for the session.
func (ct *CostTracker) TotalCost() float64 {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.totalCostUSD
}

// TotalTokens returns total tokens used across all models.
func (ct *CostTracker) TotalTokens() int64 {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.totalPromptTokens + ct.totalCompletionTokens
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

	totalTokens := ct.totalPromptTokens + ct.totalCompletionTokens
	sb.WriteString("\n  Tokens:\n")
	sb.WriteString(fmt.Sprintf("    Prompt:     %s\n", formatTokenCount64(ct.totalPromptTokens)))
	sb.WriteString(fmt.Sprintf("    Completion: %s\n", formatTokenCount64(ct.totalCompletionTokens)))
	sb.WriteString(fmt.Sprintf("    Total:      %s\n", formatTokenCount64(totalTokens)))

	if ct.totalCacheCreation > 0 || ct.totalCacheRead > 0 {
		sb.WriteString("\n  Cache tokens:\n")
		sb.WriteString(fmt.Sprintf("    Created:    %s\n", formatTokenCount64(ct.totalCacheCreation)))
		sb.WriteString(fmt.Sprintf("    Read:       %s\n", formatTokenCount64(ct.totalCacheRead)))
	}

	if ct.totalCostUSD > 0 {
		sb.WriteString("\n  Estimated cost:\n")
		hasReal := false
		for _, rec := range ct.modelUsage {
			if rec.HasRealData {
				hasReal = true
				break
			}
		}
		accuracy := "(character-based estimate)"
		if hasReal {
			accuracy = "(from API usage data)"
		}
		sb.WriteString(fmt.Sprintf("    Total:  %s %s\n",
			colorize(fmt.Sprintf("$%.4f", ct.totalCostUSD), ColorGreen),
			colorize(accuracy, ColorGray)))
	} else {
		sb.WriteString("\n  (Pricing not available for this model)\n")
	}

	// Budget status
	if msg := ct.budgetMessageLocked(); msg != "" {
		sb.WriteString(fmt.Sprintf("\n  %s\n", msg))
	}

	// Per-model breakdown if multiple models used
	if len(ct.modelUsage) > 1 {
		sb.WriteString("\n  Per-model breakdown:\n")
		for _, rec := range ct.modelUsage {
			sb.WriteString(fmt.Sprintf("    %s/%s: %s tokens, $%.4f (%d requests)\n",
				rec.Provider, rec.Model,
				formatTokenCount64(rec.TotalTokens),
				rec.TotalCostUSD, rec.Requests))
		}
	}

	return sb.String()
}

// SaveSession persists the current cost data to disk for cross-session tracking.
func (ct *CostTracker) SaveSession() error {
	ct.mu.RLock()
	data := SessionCostData{
		SessionID:     ct.sessionID,
		StartTime:     ct.sessionStart,
		LastUpdate:    ct.lastUpdate,
		ModelUsage:    ct.modelUsage,
		TotalCostUSD:  ct.totalCostUSD,
		TotalRequests: ct.totalRequests,
	}
	ct.mu.RUnlock()

	dir := costSessionDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	path := filepath.Join(dir, ct.sessionID+".json")
	return os.WriteFile(path, b, 0o600)
}

// RestoreSession loads a previous session's cost data.
func (ct *CostTracker) RestoreSession(sessionID string) error {
	path := filepath.Join(costSessionDir(), sessionID+".json")
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}

	var data SessionCostData
	if err := json.Unmarshal(b, &data); err != nil {
		return fmt.Errorf("unmarshal session: %w", err)
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.sessionID = data.SessionID
	ct.sessionStart = data.StartTime
	ct.lastUpdate = data.LastUpdate
	ct.modelUsage = data.ModelUsage
	if ct.modelUsage == nil {
		ct.modelUsage = make(map[string]*ModelUsageRecord)
	}
	ct.recomputeAggregates()
	return nil
}

// --- Internal helpers ---

func (ct *CostTracker) getOrCreateRecord(key, provider, model string) *ModelUsageRecord {
	rec, ok := ct.modelUsage[key]
	if !ok {
		rec = &ModelUsageRecord{
			Provider: provider,
			Model:    model,
		}
		ct.modelUsage[key] = rec
	}
	return rec
}

func (ct *CostTracker) recomputeCost(rec *ModelUsageRecord) {
	inputCost, outputCost := getModelPricing(rec.Provider, rec.Model)
	cacheWriteCost, cacheReadCost := getCachePricing(rec.Provider, rec.Model)

	rec.InputCostUSD = float64(rec.PromptTokens) / 1_000_000 * inputCost
	rec.OutputCostUSD = float64(rec.CompletionTokens) / 1_000_000 * outputCost
	rec.CacheCostUSD = float64(rec.CacheCreationTokens)/1_000_000*cacheWriteCost +
		float64(rec.CacheReadTokens)/1_000_000*cacheReadCost
	rec.TotalCostUSD = rec.InputCostUSD + rec.OutputCostUSD + rec.CacheCostUSD
}

func (ct *CostTracker) recomputeAggregates() {
	ct.totalPromptTokens = 0
	ct.totalCompletionTokens = 0
	ct.totalCacheCreation = 0
	ct.totalCacheRead = 0
	ct.totalRequests = 0
	ct.totalCostUSD = 0

	for _, rec := range ct.modelUsage {
		ct.totalPromptTokens += rec.PromptTokens
		ct.totalCompletionTokens += rec.CompletionTokens
		ct.totalCacheCreation += rec.CacheCreationTokens
		ct.totalCacheRead += rec.CacheReadTokens
		ct.totalRequests += rec.Requests
		ct.totalCostUSD += rec.TotalCostUSD
	}
}

func (ct *CostTracker) budgetMessageLocked() string {
	if ct.budgetLimitUSD <= 0 {
		return ""
	}
	pct := ct.totalCostUSD / ct.budgetLimitUSD * 100
	if ct.totalCostUSD >= ct.budgetLimitUSD {
		return fmt.Sprintf("BUDGET EXCEEDED: $%.4f / $%.2f (%.0f%%)", ct.totalCostUSD, ct.budgetLimitUSD, pct)
	}
	if ct.totalCostUSD >= ct.budgetLimitUSD*ct.budgetWarningPct {
		return fmt.Sprintf("Budget warning: $%.4f / $%.2f (%.0f%%)", ct.totalCostUSD, ct.budgetLimitUSD, pct)
	}
	return ""
}

func costSessionDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".chatcli", "sessions")
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

// --- Pricing tables ---

// getModelPricing returns input and output cost per 1M tokens for known models.
// Prices in USD per 1M tokens.
//
// Dispatches to per-family helpers so each family can evolve independently
// without the function blowing past the project's cyclomatic budget. The
// helpers are tried in priority order: model-id heuristics first (model is
// authoritative for cloud providers), then provider-string fallbacks for
// wrappers and self-hosted backends.
func getModelPricing(provider, model string) (inputCost, outputCost float64) {
	model = strings.ToLower(model)
	provider = strings.ToLower(provider)

	// DEVIN antes das heurísticas de modelo: o wrapper roteia modelos com
	// nomes reconhecíveis (claude-*, gpt-*) mas o binário não reporta
	// tokens e o custo é da assinatura Cognition — sem o curto-circuito,
	// claudePricing/openAIPricing cobrariam como se fosse API direta.
	if strings.Contains(provider, "devin") {
		return 0, 0
	}

	for _, fn := range []func(string) (float64, float64, bool){
		claudePricing,
		openAIPricing,
		googlePricing,
		grokPricing,
		deepseekPricing,
		zaiPricing,
	} {
		if in, out, ok := fn(model); ok {
			return in, out
		}
	}
	return providerFallbackPricing(provider, model)
}

func claudePricing(model string) (float64, float64, bool) {
	if !strings.Contains(model, "claude") {
		return 0, 0, false
	}
	switch {
	case strings.Contains(model, "fable"):
		// Fable 5: $10/$50 per MTok (tier above Opus).
		return 10.0, 50.0, true
	case strings.Contains(model, "opus-5"):
		// Opus 5 (Jul 2026): keeps the $5/$25 Opus-tier price. Must match
		// BEFORE the generic "opus" case below, which is the $15/$75
		// legacy tier — the substring also covers the Bedrock
		// (anthropic.claude-opus-5) and OpenRouter (anthropic/claude-opus-5)
		// spellings.
		return 5.0, 25.0, true
	case strings.Contains(model, "opus-4-5"), strings.Contains(model, "opus-4-6"),
		strings.Contains(model, "opus-4-7"), strings.Contains(model, "opus-4-8"):
		// Opus 4.5 onward dropped to $5/$25 per MTok; the 1M context on
		// 4.6+ carries no long-context premium.
		return 5.0, 25.0, true
	case strings.Contains(model, "opus"):
		// Opus 3 / 4.0 / 4.1 legacy pricing.
		return 15.0, 75.0, true
	case strings.Contains(model, "sonnet"):
		return 3.0, 15.0, true
	case strings.Contains(model, "haiku-4-5"):
		return 1.0, 5.0, true
	case strings.Contains(model, "haiku"):
		return 0.25, 1.25, true
	}
	return 0, 0, false
}

// openAIPricing covers both the GPT-* and o-* reasoning families. Ordering
// matters: more specific tags ("gpt-4o-mini") must come before their parents
// ("gpt-4o").
func openAIPricing(model string) (float64, float64, bool) {
	switch {
	// gpt-5.6 (Jul 2026): preços de lista da API por tier, já refletindo
	// os cortes de 30/Jul/2026 (terra −20%, luna −80%; Sol inalterado —
	// developers.openai.com/docs/pricing). O caso genérico "gpt-5.6"
	// cobre o alias de família (que o catálogo resolve para Sol) e
	// precisa vir depois dos tiers específicos. O surcharge de
	// long-context (>272K input = 2×/1,5×) não é modelado — cost_tracker
	// trabalha com um único tier por modelo.
	case strings.Contains(model, "gpt-5.6-terra"):
		return 2.0, 12.0, true
	case strings.Contains(model, "gpt-5.6-luna"):
		return 0.20, 1.20, true
	case strings.Contains(model, "gpt-5.6"):
		return 5.0, 30.0, true
	case strings.Contains(model, "gpt-4o-mini"):
		return 0.15, 0.60, true
	case strings.Contains(model, "gpt-4o"):
		return 2.50, 10.0, true
	case strings.Contains(model, "gpt-4-turbo"):
		return 10.0, 30.0, true
	case strings.Contains(model, "gpt-4.1"):
		return 2.0, 8.0, true
	case strings.Contains(model, "gpt-4"):
		return 30.0, 60.0, true
	case strings.Contains(model, "gpt-3.5"):
		return 0.50, 1.50, true
	case strings.Contains(model, "o3-mini"), strings.Contains(model, "o4-mini"):
		return 1.10, 4.40, true
	case strings.Contains(model, "o3"):
		return 10.0, 40.0, true
	case strings.Contains(model, "o1-mini"):
		return 3.0, 12.0, true
	case strings.Contains(model, "o1"):
		return 15.0, 60.0, true
	}
	return 0, 0, false
}

// googlePricing cobre as gerações Gemini 3.x/2.x/1.5 (ai.google.dev
// pricing, Aug 2026). Ordering: tags específicas antes das genéricas —
// "gemini-3" (Pro/preview) por último no bloco 3.x porque é substring de
// todos os ids 3.x; "gemini-2.5-flash-lite" antes de "gemini-2.5-flash".
// 3.7/3.6-flash usam o preço introdutório vigente ($0.75/$3.75 até
// 31/Dez/2026; dobra a partir de Jan/2027 — revisar na virada).
func googlePricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "gemini-3.7-flash"), strings.Contains(model, "gemini-3.6-flash"):
		return 0.75, 3.75, true
	case strings.Contains(model, "gemini-3.5-flash-lite"):
		return 0.30, 2.50, true
	case strings.Contains(model, "gemini-3.5-flash"):
		return 1.50, 9.0, true
	case strings.Contains(model, "gemini-3.1-pro"):
		return 2.0, 12.0, true
	case strings.Contains(model, "gemini-3.1-flash-lite"):
		return 0.25, 1.50, true
	case strings.Contains(model, "gemini-3-flash"):
		return 0.50, 3.0, true
	case strings.Contains(model, "gemini-3"):
		// gemini-3 / gemini-3-pro(-preview) — mesmo tier do 3.1 Pro.
		return 2.0, 12.0, true
	case strings.Contains(model, "gemini-2.5-pro"):
		return 1.25, 10.0, true
	case strings.Contains(model, "gemini-2.5-flash-lite"):
		return 0.10, 0.40, true
	case strings.Contains(model, "gemini-2.5-flash"):
		return 0.30, 2.50, true
	case strings.Contains(model, "gemini-2.0"):
		return 0.075, 0.30, true
	case strings.Contains(model, "gemini-1.5-pro"):
		return 1.25, 5.0, true
	case strings.Contains(model, "gemini-1.5-flash"):
		return 0.075, 0.30, true
	}
	return 0, 0, false
}

// grokPricing cobre a geração 2026 (docs.x.ai/docs/pricing, Aug 2026).
// A xAI cobra por tier de prompt (<200K / ≥200K); cost_tracker modela um
// único tier, então usamos o tier base (<200K) — consistente com o
// tratamento de long-context da OpenAI acima. Específicos antes do
// genérico "grok".
func grokPricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "grok-4.6"), strings.Contains(model, "grok-4.5"):
		return 2.0, 6.0, true
	case strings.Contains(model, "grok-4.3"), strings.Contains(model, "grok-4.20"):
		return 1.25, 2.50, true
	case strings.Contains(model, "grok-build"):
		return 1.0, 2.0, true
	case strings.Contains(model, "grok-3"):
		return 3.0, 15.0, true
	case strings.Contains(model, "grok-2"):
		return 2.0, 10.0, true
	case strings.Contains(model, "grok"):
		return 5.0, 15.0, true
	}
	return 0, 0, false
}

// zaiPricing covers Z.AI's GLM-5 family. Public list prices (docs.z.ai,
// Jun 2026): GLM-5.2 $1.40/$4.40, GLM-5 $1.00/$3.20 per MTok. Ordering
// matters: the specific "glm-5.2" tag must win before the bare "glm-5"
// prefix. GLM-4.x and other Z.AI ids fall through to the conservative
// flat rate in providerFallbackPricing.
func zaiPricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "glm-5.2"), strings.Contains(model, "glm-5-2"):
		return 1.40, 4.40, true
	case strings.Contains(model, "glm-5.1"), strings.Contains(model, "glm-5-1"):
		// GLM-5.1: mesmo tier da 5.2 no pricing oficial (docs.z.ai).
		return 1.40, 4.40, true
	case strings.Contains(model, "glm-5-turbo"), strings.Contains(model, "glm-5v-turbo"):
		return 1.20, 4.00, true
	case strings.Contains(model, "glm-5"):
		return 1.00, 3.20, true
	}
	return 0, 0, false
}

// deepseekPricing — geração V4 (api-docs.deepseek.com, Aug 2026). A
// DeepSeek cobra peak/off-peak (off-peak = metade); cost_tracker usa o
// preço de pico para não sub-reportar. V4 específicos antes dos legados.
func deepseekPricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "deepseek-v4-pro"):
		return 1.32, 3.96, true
	case strings.Contains(model, "deepseek-v4"):
		// deepseek-v4-flash e futuros ids v4 sem tier próprio.
		return 0.44, 1.32, true
	case strings.Contains(model, "deepseek-r1"):
		return 0.55, 2.19, true
	case strings.Contains(model, "deepseek"):
		return 0.27, 1.10, true
	}
	return 0, 0, false
}

// providerFallbackPricing handles families whose model IDs are ambiguous
// or where the provider name is the most reliable signal (proprietary
// wrappers like Copilot, local backends like Ollama).
//
// Moonshot (Kimi) — kimi-k3 public list price as of 2026-07 is $3.00/M
// input (cache miss; cache hit is $0.30/M) and $15.00/M output — priced
// well above the K2 line, so it gets its own case BEFORE the generic kimi
// match (specific beats generic, same ordering rule as claudePricing).
// kimi-k2.6 as of 2026-05 is $0.95/M input (cache miss) and $4.00/M
// output; cache-hit input is $0.16/M but cost_tracker only models a
// single tier — we charge the miss price so accounting stays
// conservative. K2.5 and moonshot-v1-* sit below K2.6; we approximate
// with K2.6 numbers to avoid under-reporting.
func providerFallbackPricing(provider, model string) (float64, float64) {
	switch {
	case strings.Contains(model, "minimax"), strings.Contains(provider, "minimax"):
		return 0.20, 1.10
	case strings.Contains(provider, "zai"), strings.Contains(model, "glm"):
		return 0.50, 0.50
	case strings.HasPrefix(model, "kimi-k3"):
		return 3.00, 15.00
	case strings.HasPrefix(model, "kimi-k2.7-code-highspeed"):
		// K2.7 Code highspeed (platform.kimi.ai pricing, Aug 2026): 2× o
		// tier padrão K2.x — specific beats generic, senão o match "kimi"
		// abaixo cobraria a metade.
		return 1.90, 8.00
	case strings.Contains(provider, "moonshot"), strings.HasPrefix(model, "kimi"), strings.HasPrefix(model, "moonshot"):
		// Cobre também kimi-k2.7-code ($0.95/$4.00 — mesmo tier do K2.6).
		return 0.95, 4.00
	case strings.Contains(provider, "copilot"):
		return 2.50, 10.0
	case strings.Contains(provider, "openrouter"):
		return getOpenRouterModelPricing(model)
	case strings.Contains(provider, "ollama"), strings.Contains(provider, "stackspot"),
		strings.Contains(provider, "devin"):
		// Devin CLI: o binário não reporta tokens e o custo é da assinatura
		// Cognition — zero aqui, como Ollama/StackSpot.
		return 0, 0
	}
	return 0, 0
}

// getCachePricing returns cache write and cache read cost per 1M tokens.
// Currently only Anthropic supports prompt caching with distinct pricing.
func getCachePricing(provider, model string) (cacheWriteCost, cacheReadCost float64) {
	model = strings.ToLower(model)

	if !strings.Contains(model, "claude") {
		return 0, 0
	}

	// Anthropic cache pricing: write = 1.25x input, read = 0.1x input
	inputCost, _ := getModelPricing(provider, model)
	return inputCost * 1.25, inputCost * 0.10
}

// getOpenRouterModelPricing returns pricing for models accessed via OpenRouter.
func getOpenRouterModelPricing(model string) (inputCost, outputCost float64) {
	// OpenRouter passes through pricing from upstream providers.
	// Try to match the underlying model.
	switch {
	case strings.Contains(model, "claude"):
		return getModelPricing("anthropic", model)
	case strings.Contains(model, "gpt"):
		return getModelPricing("openai", model)
	case strings.Contains(model, "gemini"):
		return getModelPricing("google", model)
	case strings.Contains(model, "deepseek"):
		return getModelPricing("deepseek", model)
	case strings.Contains(model, "llama"):
		return 0.20, 0.20
	case strings.Contains(model, "mistral"):
		return 0.20, 0.60
	case strings.Contains(model, "qwen"):
		return 0.15, 0.15
	}
	return 0, 0
}
