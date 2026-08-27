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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/i18n"
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

// costSnapshotRetention is how long persisted cost snapshots are kept before
// being pruned on save — aligned with the session TTL default (90 days).
const costSnapshotRetention = 90 * 24 * time.Hour

// costSaveThrottle bounds how often the write-through snapshot hits disk.
// Recording is per turn; the file is tiny, but there is no reason to fsync
// faster than a human can read /cost.
const costSaveThrottle = 2 * time.Second

// ModelUsageRecord tracks cumulative token usage and cost for a single model.
type ModelUsageRecord struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`

	// Core token counts
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`

	// Prompt-cache tokens. Anthropic reports them ALONGSIDE input_tokens
	// (additive); OpenAI/Gemini report cache reads as a SUBSET of the
	// prompt count — recomputeCost handles both semantics.
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int64 `json:"cache_read_tokens,omitempty"`

	// Reasoning tokens (o-series / GPT-5 / Gemini thinking). Informational:
	// already billed inside CompletionTokens.
	ReasoningTokens int64 `json:"reasoning_tokens,omitempty"`

	// Tracking
	Requests    int  `json:"requests"`
	HasRealData bool `json:"has_real_data"` // true if at least one call returned API usage

	// PricingKnown is false when the model matched no pricing table entry —
	// the computed cost is then zero NOT because the model is free but
	// because ChatCLI does not know its price. /cost surfaces the difference.
	PricingKnown bool `json:"pricing_known"`

	// ProviderCostUSD accumulates the actually-billed cost reported by the
	// provider itself (OpenRouter usage.cost) — authoritative for the calls
	// that carried it. The Billed* pools remember WHICH tokens those calls
	// covered, so a mixed key (some calls report usage.cost, some do not)
	// prices only the uncovered remainder from the tables instead of
	// discarding it: TotalCostUSD = table(unbilled tokens) + ProviderCostUSD.
	ProviderCostUSD           float64 `json:"provider_cost_usd,omitempty"`
	BilledPromptTokens        int64   `json:"billed_prompt_tokens,omitempty"`
	BilledCompletionTokens    int64   `json:"billed_completion_tokens,omitempty"`
	BilledCacheReadTokens     int64   `json:"billed_cache_read_tokens,omitempty"`
	BilledCacheCreationTokens int64   `json:"billed_cache_creation_tokens,omitempty"`

	// Computed cost (in USD)
	InputCostUSD  float64 `json:"input_cost_usd"`
	OutputCostUSD float64 `json:"output_cost_usd"`
	CacheCostUSD  float64 `json:"cache_cost_usd"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
}

// SessionCostData is the serializable snapshot of a cost tracking session.
type SessionCostData struct {
	SessionID     string                       `json:"session_id"`
	SessionName   string                       `json:"session_name,omitempty"`
	StartTime     time.Time                    `json:"start_time"`
	LastUpdate    time.Time                    `json:"last_update"`
	ModelUsage    map[string]*ModelUsageRecord `json:"model_usage"` // key: "provider:model"
	TotalCostUSD  float64                      `json:"total_cost_usd"`
	TotalRequests int                          `json:"total_requests"`
	TotalTokens   int64                        `json:"total_tokens,omitempty"`
}

// CostTracker tracks token usage and estimated cost for the current session,
// with per-model granularity, real API usage data support, cache token pricing,
// write-through session persistence, and configurable budget enforcement.
type CostTracker struct {
	mu sync.RWMutex

	sessionID    string
	sessionName  string
	sessionStart time.Time
	lastUpdate   time.Time

	// Per-model usage: key is "provider:model"
	modelUsage map[string]*ModelUsageRecord

	// Aggregates (computed from modelUsage)
	totalPromptTokens     int64
	totalCompletionTokens int64
	totalCacheCreation    int64
	totalCacheRead        int64
	totalReasoning        int64
	totalRequests         int
	totalCostUSD          float64

	// Budget enforcement
	budgetLimitUSD   float64 // 0 = no limit
	budgetWarningPct float64 // fraction (0.8 = 80%)
	budgetHardStop   bool    // refuse new LLM turns once exceeded

	// lastAnnouncedLevel arms the one-shot proactive budget notice: a
	// transition is reported once per escalation, not on every turn.
	lastAnnouncedLevel BudgetLevel

	// Persistence write-through throttle.
	lastSave time.Time

	// For backward compat display
	lastProvider string
	lastModel    string
}

// NewCostTracker creates a new cost tracker with optional budget limit.
func NewCostTracker() *CostTracker {
	ct := &CostTracker{
		sessionID:    newCostSessionID(time.Now()),
		sessionStart: time.Now(),
		lastUpdate:   time.Now(),
		modelUsage:   make(map[string]*ModelUsageRecord),
	}
	ct.loadBudgetFromEnvLocked()
	return ct
}

// costSessionSeq disambiguates ids minted in the same second by the same
// process (e.g. /cost reset issued twice quickly).
var costSessionSeq atomic.Int64

// newCostSessionID builds a human-sortable snapshot id: start timestamp plus
// pid (two CLIs started in the same second must not clobber each other) plus
// a per-process sequence for same-second resets.
func newCostSessionID(t time.Time) string {
	id := t.Format("20060102-150405") + "-" + strconv.Itoa(os.Getpid())
	if seq := costSessionSeq.Add(1); seq > 1 {
		id += "-" + strconv.FormatInt(seq, 10)
	}
	return id
}

// loadBudgetFromEnvLocked (re)reads the budget environment variables.
// Caller must hold ct.mu (or be the constructor).
func (ct *CostTracker) loadBudgetFromEnvLocked() {
	ct.budgetLimitUSD = 0
	if v := os.Getenv("CHATCLI_SESSION_BUDGET_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			ct.budgetLimitUSD = f
		}
	}

	ct.budgetWarningPct = 0.80
	if v := os.Getenv("CHATCLI_BUDGET_WARNING_PCT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			ct.budgetWarningPct = f
		}
	}

	ct.budgetHardStop = false
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_BUDGET_HARD_STOP"))); v != "" {
		ct.budgetHardStop = v == "1" || v == "true" || v == "on" || v == "yes"
	}
}

// ReloadBudget re-reads the budget environment variables so /reload picks up
// .env changes without restarting the process.
func (ct *CostTracker) ReloadBudget() {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.loadBudgetFromEnvLocked()
	// Re-arm the proactive notice against the new limits.
	ct.lastAnnouncedLevel = ct.budgetLevelLocked()
}

// SetSessionName attaches the named-session identity to the persisted
// snapshot so /cost sessions can show which conversation the spend belongs to.
func (ct *CostTracker) SetSessionName(name string) {
	ct.mu.Lock()
	ct.sessionName = name
	ct.mu.Unlock()
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

	key := modelKey(provider, model)
	rec := ct.getOrCreateRecord(key, provider, model)

	rec.PromptTokens += int64(usage.PromptTokens)
	rec.CompletionTokens += int64(usage.CompletionTokens)
	totalTokens := usage.TotalTokens
	if totalTokens == 0 {
		// Providers that omit total_tokens must not zero the record's total.
		totalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	rec.TotalTokens += int64(totalTokens)
	rec.CacheCreationTokens += int64(usage.CacheCreationInputTokens)
	rec.CacheReadTokens += int64(usage.CacheReadInputTokens)
	rec.ReasoningTokens += int64(usage.ReasoningTokens)
	if usage.CostUSD > 0 {
		// This call's tokens are covered by the provider-billed amount —
		// remember them so recomputeCost prices only the uncovered rest.
		rec.ProviderCostUSD += usage.CostUSD
		rec.BilledPromptTokens += int64(usage.PromptTokens)
		rec.BilledCompletionTokens += int64(usage.CompletionTokens)
		rec.BilledCacheReadTokens += int64(usage.CacheReadInputTokens)
		rec.BilledCacheCreationTokens += int64(usage.CacheCreationInputTokens)
	}
	rec.Requests++
	if usage.IsReal {
		rec.HasRealData = true
	}

	// Compute cost for this increment
	recomputeRecordCost(rec)
	ct.recomputeAggregates()

	ct.lastProvider = provider
	ct.lastModel = model
	ct.lastUpdate = time.Now()

	shouldSave := time.Since(ct.lastSave) >= costSaveThrottle
	if shouldSave {
		ct.lastSave = time.Now()
	}
	ct.mu.Unlock()

	if shouldSave {
		_ = ct.SaveSession()
	}
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

// Reset closes the current accounting period and starts a fresh one. The
// closing period is persisted first so /cost last and /cost sessions can
// still see it — resetting never discards data.
func (ct *CostTracker) Reset() {
	_ = ct.SaveSession()

	ct.mu.Lock()
	defer ct.mu.Unlock()
	now := time.Now()
	ct.sessionID = newCostSessionID(now)
	ct.sessionStart = now
	ct.lastUpdate = now
	ct.modelUsage = make(map[string]*ModelUsageRecord)
	ct.recomputeAggregates()
	ct.lastAnnouncedLevel = BudgetOK
	ct.lastSave = time.Time{}
}

// CheckBudget returns the current budget level.
func (ct *CostTracker) CheckBudget() BudgetLevel {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.budgetLevelLocked()
}

// BudgetBlocked reports whether new LLM turns must be refused: a budget is
// configured, CHATCLI_BUDGET_HARD_STOP is on, and the limit is exhausted.
func (ct *CostTracker) BudgetBlocked() bool {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.budgetHardStop && ct.budgetLimitUSD > 0 && ct.totalCostUSD >= ct.budgetLimitUSD
}

// BudgetHardStopEnabled reports whether the hard-stop gate is armed.
func (ct *CostTracker) BudgetHardStopEnabled() bool {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.budgetHardStop
}

// TakeBudgetTransition returns a one-shot notice when the budget level has
// escalated since the last check (OK→Warning, Warning→Exceeded, …). The
// returned message is already localized; ok is false when there is nothing
// new to announce. De-escalations (after /cost reset or a raised limit)
// re-arm the notice silently.
func (ct *CostTracker) TakeBudgetTransition() (BudgetLevel, string, bool) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	level := ct.budgetLevelLocked()
	if level == ct.lastAnnouncedLevel {
		return level, "", false
	}
	escalated := level > ct.lastAnnouncedLevel
	ct.lastAnnouncedLevel = level
	if !escalated {
		return level, "", false
	}
	return level, ct.budgetMessageLocked(), true
}

// BudgetMessage returns a human-readable budget status message.
// Returns empty string if no budget is configured or if spending is within limits.
func (ct *CostTracker) BudgetMessage() string {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.budgetMessageLocked()
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

// Snapshot returns a copy of the current session cost data — the same shape
// that is persisted to disk, safe for the caller to serialize or render.
func (ct *CostTracker) Snapshot() SessionCostData {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.snapshotLocked()
}

func (ct *CostTracker) snapshotLocked() SessionCostData {
	usage := make(map[string]*ModelUsageRecord, len(ct.modelUsage))
	for k, rec := range ct.modelUsage {
		cp := *rec
		usage[k] = &cp
	}
	return SessionCostData{
		SessionID:     ct.sessionID,
		SessionName:   ct.sessionName,
		StartTime:     ct.sessionStart,
		LastUpdate:    ct.lastUpdate,
		ModelUsage:    usage,
		TotalCostUSD:  ct.totalCostUSD,
		TotalRequests: ct.totalRequests,
		TotalTokens:   ct.totalPromptTokens + ct.totalCompletionTokens,
	}
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

// --- Persistence ---

// SaveSession persists the current cost data to disk for cross-session
// tracking (write-through from RecordRealUsage, plus explicit calls on
// reset/shutdown). Snapshots older than the retention window are pruned.
func (ct *CostTracker) SaveSession() error {
	ct.mu.RLock()
	data := ct.snapshotLocked()
	ct.mu.RUnlock()

	if data.TotalRequests == 0 {
		return nil // nothing worth persisting
	}

	dir := costStoreDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cost store dir: %w", err)
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cost session: %w", err)
	}

	// Atomic write with a UNIQUE temp name: concurrent saves (worker
	// recorder goroutines race the main turn) must never interleave writes
	// into one shared temp file, and a crash mid-write must never corrupt
	// the snapshot.
	path := filepath.Join(dir, data.SessionID+".json")
	tmp, err := os.CreateTemp(dir, data.SessionID+"-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}

	pruneCostSnapshots(dir)
	return nil
}

// RestoreSession loads a previous session's cost data into the tracker.
func (ct *CostTracker) RestoreSession(sessionID string) error {
	data, err := LoadCostSnapshot(sessionID)
	if err != nil {
		return err
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.sessionID = data.SessionID
	ct.sessionName = data.SessionName
	ct.sessionStart = data.StartTime
	ct.lastUpdate = data.LastUpdate
	ct.modelUsage = data.ModelUsage
	if ct.modelUsage == nil {
		ct.modelUsage = make(map[string]*ModelUsageRecord)
	}
	ct.recomputeAggregates()
	return nil
}

// LoadCostSnapshot reads one persisted snapshot by session id.
func LoadCostSnapshot(sessionID string) (*SessionCostData, error) {
	path := filepath.Join(costStoreDir(), filepath.Base(sessionID)+".json")
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var data SessionCostData
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("unmarshal cost session: %w", err)
	}
	return &data, nil
}

// ListCostSnapshots returns persisted snapshots, most recent first, capped
// at limit (0 = no cap). The current process's snapshot is included when it
// has been written.
func ListCostSnapshots(limit int) ([]*SessionCostData, error) {
	dir := costStoreDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	out := make([]*SessionCostData, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := LoadCostSnapshot(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue // unreadable snapshot: skip, never break the listing
		}
		out = append(out, data)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUpdate.After(out[j].LastUpdate) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CurrentSessionID returns the id under which this session's snapshot is
// persisted.
func (ct *CostTracker) CurrentSessionID() string {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.sessionID
}

// pruneCostSnapshots removes snapshots older than the retention window.
// Best-effort: pruning must never fail a save.
func pruneCostSnapshots(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-costSnapshotRetention)
	// Stray .tmp files (a crash between write and rename) age out on a much
	// shorter fuse — they are garbage the moment their writer is gone.
	tmpCutoff := time.Now().Add(-time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		isJSON := strings.HasSuffix(e.Name(), ".json")
		isTmp := strings.HasSuffix(e.Name(), ".tmp")
		if !isJSON && !isTmp {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if (isJSON && info.ModTime().Before(cutoff)) || (isTmp && info.ModTime().Before(tmpCutoff)) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
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

// recomputeRecordCost prices one record in place — the ONLY cost formula
// in the tracker; estimateTurnCostUSD delegates here so per-turn and
// per-session math can never disagree.
func recomputeRecordCost(rec *ModelUsageRecord) {
	inputCost, outputCost, known := lookupModelPricing(rec.Provider, rec.Model)
	rec.PricingKnown = known
	cacheWriteCost, cacheReadCost := getCachePricing(rec.Provider, rec.Model)

	// Table math prices only the tokens NOT covered by a provider-billed
	// amount (usage.cost): billed calls' tokens live in the Billed* pools
	// and their cost is ProviderCostUSD verbatim. A mixed key (some calls
	// report cost, some don't) therefore adds both parts instead of letting
	// one clobber the other.
	unbilled := func(total, billed int64) int64 {
		if d := total - billed; d > 0 {
			return d
		}
		return 0
	}
	promptTokens := unbilled(rec.PromptTokens, rec.BilledPromptTokens)
	completionTokens := unbilled(rec.CompletionTokens, rec.BilledCompletionTokens)
	cacheRead := unbilled(rec.CacheReadTokens, rec.BilledCacheReadTokens)
	cacheCreation := unbilled(rec.CacheCreationTokens, rec.BilledCacheCreationTokens)

	billableInput := promptTokens
	if !cacheTokensAdditive(rec.Provider, rec.Model) && cacheRead > 0 && cacheReadCost > 0 {
		// OpenAI/Gemini-style usage reports cached tokens as a SUBSET of the
		// prompt count — carve them out so they are billed once, at the
		// discounted cache-read rate, instead of twice. Only when a discount
		// rate exists: for families without a published cache rate the
		// carve-out would make cached tokens FREE, so they stay billed at
		// the plain input price instead (conservative).
		billableInput -= cacheRead
		if billableInput < 0 {
			billableInput = 0
		}
	}

	rec.InputCostUSD = float64(billableInput) / 1_000_000 * inputCost
	rec.OutputCostUSD = float64(completionTokens) / 1_000_000 * outputCost
	rec.CacheCostUSD = float64(cacheCreation)/1_000_000*cacheWriteCost +
		float64(cacheRead)/1_000_000*cacheReadCost
	rec.TotalCostUSD = rec.InputCostUSD + rec.OutputCostUSD + rec.CacheCostUSD + rec.ProviderCostUSD
}

func (ct *CostTracker) recomputeAggregates() {
	ct.totalPromptTokens = 0
	ct.totalCompletionTokens = 0
	ct.totalCacheCreation = 0
	ct.totalCacheRead = 0
	ct.totalReasoning = 0
	ct.totalRequests = 0
	ct.totalCostUSD = 0

	for _, rec := range ct.modelUsage {
		ct.totalPromptTokens += rec.PromptTokens
		ct.totalCompletionTokens += rec.CompletionTokens
		ct.totalCacheCreation += rec.CacheCreationTokens
		ct.totalCacheRead += rec.CacheReadTokens
		ct.totalReasoning += rec.ReasoningTokens
		ct.totalRequests += rec.Requests
		ct.totalCostUSD += rec.TotalCostUSD
	}
}

func (ct *CostTracker) budgetLevelLocked() BudgetLevel {
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

func (ct *CostTracker) budgetMessageLocked() string {
	if ct.budgetLimitUSD <= 0 {
		return ""
	}
	pct := ct.totalCostUSD / ct.budgetLimitUSD * 100
	switch ct.budgetLevelLocked() {
	case BudgetExceeded:
		if ct.budgetHardStop {
			return i18n.T("cost.budget.exceeded_hard", ct.totalCostUSD, ct.budgetLimitUSD, pct)
		}
		return i18n.T("cost.budget.exceeded", ct.totalCostUSD, ct.budgetLimitUSD, pct)
	case BudgetWarning:
		return i18n.T("cost.budget.warning", ct.totalCostUSD, ct.budgetLimitUSD, pct)
	default:
		return ""
	}
}

func costStoreDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".chatcli", "costs")
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

// estimateTurnCostUSD prices a single turn's usage with the same rules the
// session tracker applies (cache semantics included), so the chat envelope
// footer and /cost never disagree about the same turn. A provider-reported
// cost wins outright.
func estimateTurnCostUSD(provider, model string, usage *models.UsageInfo) float64 {
	if usage == nil {
		return 0
	}
	if usage.CostUSD > 0 {
		return usage.CostUSD
	}
	// Single source of truth: run the turn through the exact record math the
	// session tracker applies — a second hand-rolled formula here is how the
	// footer and /cost drift apart.
	rec := &ModelUsageRecord{
		Provider:            provider,
		Model:               model,
		PromptTokens:        int64(usage.PromptTokens),
		CompletionTokens:    int64(usage.CompletionTokens),
		CacheReadTokens:     int64(usage.CacheReadInputTokens),
		CacheCreationTokens: int64(usage.CacheCreationInputTokens),
	}
	recomputeRecordCost(rec)
	return rec.TotalCostUSD
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
	in, out, _ := lookupModelPricing(provider, model)
	return in, out
}

// lookupModelPricing is getModelPricing plus a known flag: known=false means
// the model matched NO table entry — cost zero because the price is unknown,
// not because the backend is unmetered. Ollama/StackSpot/Devin return
// known=true with zero prices (deliberately free from ChatCLI's viewpoint).
func lookupModelPricing(provider, model string) (inputCost, outputCost float64, known bool) {
	model = strings.ToLower(model)
	provider = strings.ToLower(provider)

	// DEVIN antes das heurísticas de modelo: o wrapper roteia modelos com
	// nomes reconhecíveis (claude-*, gpt-*) mas o binário não reporta
	// tokens e o custo é da assinatura Cognition — sem o curto-circuito,
	// claudePricing/openAIPricing cobrariam como se fosse API direta.
	if strings.Contains(provider, "devin") {
		return 0, 0, true
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
			return in, out, true
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
// Aug 2026): GLM-5.3/GLM-5.2 $1.40/$4.40, GLM-5.3-Flash $0.15/$0.50
// (list price; launch promo até 09/set/2026 não é refletida para não
// sub-reportar), GLM-5 $1.00/$3.20 per MTok. Ordering matters: the
// "-flash" tag must win before "glm-5.3", and the specific "glm-5.x"
// tags before the bare "glm-5" prefix. GLM-4.x and other Z.AI ids fall
// through to the conservative flat rate in providerFallbackPricing.
func zaiPricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "glm-5.3-flash"), strings.Contains(model, "glm-5-3-flash"):
		return 0.15, 0.50, true
	case strings.Contains(model, "glm-5.3"), strings.Contains(model, "glm-5-3"):
		return 1.40, 4.40, true
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
// "deepseek-reasoner" é o alias de API do R1 — mesma tarifa.
func deepseekPricing(model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "deepseek-v4-pro"):
		return 1.32, 3.96, true
	case strings.Contains(model, "deepseek-v4"):
		// deepseek-v4-flash e futuros ids v4 sem tier próprio.
		return 0.44, 1.32, true
	case strings.Contains(model, "deepseek-r1"), strings.Contains(model, "deepseek-reasoner"):
		return 0.55, 2.19, true
	case strings.Contains(model, "deepseek"):
		return 0.27, 1.10, true
	}
	return 0, 0, false
}

// providerFallbackPricing handles families whose model IDs are ambiguous
// or where the provider name is the most reliable signal (proprietary
// wrappers like Copilot, local backends like Ollama). The known flag is
// true for every explicit case — including the deliberately-zero backends
// (Ollama/StackSpot/Devin, unmetered from ChatCLI's viewpoint) — and false
// only on the final fallthrough, where the model is genuinely unpriced.
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
func providerFallbackPricing(provider, model string) (float64, float64, bool) {
	switch {
	case strings.Contains(model, "minimax"), strings.Contains(provider, "minimax"):
		return 0.20, 1.10, true
	case strings.Contains(provider, "zai"), strings.Contains(model, "glm"):
		return 0.50, 0.50, true
	case strings.HasPrefix(model, "kimi-k3"):
		return 3.00, 15.00, true
	case strings.HasPrefix(model, "kimi-k2.7-code-highspeed"):
		// K2.7 Code highspeed (platform.kimi.ai pricing, Aug 2026): 2× o
		// tier padrão K2.x — specific beats generic, senão o match "kimi"
		// abaixo cobraria a metade.
		return 1.90, 8.00, true
	case strings.Contains(provider, "moonshot"), strings.HasPrefix(model, "kimi"), strings.HasPrefix(model, "moonshot"):
		// Cobre também kimi-k2.7-code ($0.95/$4.00 — mesmo tier do K2.6).
		return 0.95, 4.00, true
	case strings.Contains(provider, "copilot"):
		return 2.50, 10.0, true
	case strings.Contains(provider, "openrouter"):
		return getOpenRouterModelPricing(model)
	case strings.Contains(provider, "ollama"), strings.Contains(provider, "stackspot"),
		strings.Contains(provider, "devin"):
		// Devin CLI: o binário não reporta tokens e o custo é da assinatura
		// Cognition — zero aqui, como Ollama/StackSpot.
		return 0, 0, true
	}
	return 0, 0, false
}

// cacheTokensAdditive reports whether the usage payload counts cache tokens
// ALONGSIDE the prompt count (Anthropic Messages schema: input_tokens
// excludes cache reads/writes) rather than as a subset of it (OpenAI
// cached_tokens, Gemini cachedContentTokenCount). The semantics belong to
// the REPORTING SCHEMA, not the model name: a Claude model served through
// an OpenAI-compatible gateway (OpenRouter) reports subset-style
// cached_tokens, so the provider decides when it implies the schema.
func cacheTokensAdditive(provider, model string) bool {
	if strings.Contains(strings.ToLower(provider), "openrouter") {
		return false // OpenAI-compatible schema regardless of the model
	}
	return strings.Contains(strings.ToLower(model), "claude")
}

// getCachePricing returns cache write and cache read cost per 1M tokens.
// Rates follow each provider's published discount over the model's input
// price; families without a distinct published cache rate return zero (their
// cache reads are then billed at the plain input price by recomputeCost's
// subset carve-out being a no-op).
func getCachePricing(provider, model string) (cacheWriteCost, cacheReadCost float64) {
	model = strings.ToLower(model)
	inputCost, _, known := lookupModelPricing(provider, model)
	if !known || inputCost <= 0 {
		return 0, 0
	}

	switch {
	case strings.Contains(model, "claude"):
		// Anthropic: write = 1.25x input, read = 0.1x input.
		return inputCost * 1.25, inputCost * 0.10
	case strings.Contains(model, "gemini"):
		// Google implicit/context caching: reads at 25% of input.
		return 0, inputCost * 0.25
	case strings.Contains(model, "gpt"), strings.Contains(model, "o1"),
		strings.Contains(model, "o3"), strings.Contains(model, "o4"):
		// OpenAI automatic prompt caching: hits at 50% of input, no write
		// surcharge (platform.openai.com/docs/pricing).
		return 0, inputCost * 0.50
	case strings.Contains(model, "deepseek"):
		// DeepSeek cache hit ≈ 25% of the miss price.
		return 0, inputCost * 0.25
	case strings.Contains(model, "kimi"), strings.Contains(model, "moonshot"):
		// Moonshot cache hit ($0.16/M vs $0.95/M miss) ≈ 17% of input.
		return 0, inputCost * 0.17
	}
	return 0, 0
}

// getOpenRouterModelPricing returns pricing for models accessed via OpenRouter.
// Table-derived estimates only — when the OpenRouter response carries
// usage.cost, that actual billed amount overrides these numbers entirely.
func getOpenRouterModelPricing(model string) (inputCost, outputCost float64, known bool) {
	// OpenRouter passes through pricing from upstream providers. The known
	// flag PROPAGATES from the family lookup: a slug that matches a family
	// substring but no actual pricing entry stays "unpriced" so /cost lists
	// it instead of silently reporting it as free.
	switch {
	case strings.Contains(model, "claude"):
		return lookupModelPricing("anthropic", model)
	case strings.Contains(model, "gpt"):
		return lookupModelPricing("openai", model)
	case strings.Contains(model, "gemini"):
		return lookupModelPricing("google", model)
	case strings.Contains(model, "deepseek"):
		return lookupModelPricing("deepseek", model)
	case strings.Contains(model, "llama"):
		return 0.20, 0.20, true
	case strings.Contains(model, "mistral"):
		return 0.20, 0.60, true
	case strings.Contains(model, "qwen"):
		return 0.15, 0.15, true
	}
	return 0, 0, false
}
