/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"encoding/json"
	"fmt"
	"github.com/diillson/chatcli/pkg/atrest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/pricing"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
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

	// InputTokens is the schema-normalized input (cache included) summed
	// over the record's requests — the figure that is comparable between
	// providers. PromptTokens stays exactly as the provider reported it
	// because the cost math depends on that raw split. Zero on records
	// persisted before normalization existed; readers fall back to
	// PromptTokens.
	InputTokens int64 `json:"input_tokens,omitempty"`

	// Prompt-cache tokens. Anthropic reports them ALONGSIDE input_tokens
	// (additive); OpenAI/Gemini report cache reads as a SUBSET of the
	// prompt count — recomputeCost handles both semantics.
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int64 `json:"cache_read_tokens,omitempty"`
	// CacheCreation1hTokens is the share of CacheCreationTokens written with
	// the 1-hour TTL (billed at 2x input instead of 1.25x).
	CacheCreation1hTokens int64 `json:"cache_creation_1h_tokens,omitempty"`

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

	// Explicit cache resources (Gemini cachedContents): storage billed per
	// token-hour, priced from lifecycle events (cost_cache_resources.go).
	CacheResources         int     `json:"cache_resources,omitempty"`
	CacheStorageTokenHours float64 `json:"cache_storage_token_hours,omitempty"`
	CacheStorageCostUSD    float64 `json:"cache_storage_cost_usd,omitempty"`
	EmbeddingCalls         int     `json:"embedding_calls,omitempty"`
	EmbeddingTokens        int64   `json:"embedding_tokens,omitempty"`
	EmbeddingCostUSD       float64 `json:"embedding_cost_usd,omitempty"`
	MemoryCalls            int     `json:"memory_calls,omitempty"`
	MemoryCostUSD          float64 `json:"memory_cost_usd,omitempty"`
	MemoryFactsWritten     int     `json:"memory_facts_written,omitempty"`
	MemoryEpisodesWritten  int     `json:"memory_episodes_written,omitempty"`
	MemoryRecalls          int     `json:"memory_recalls,omitempty"`
	MemoryFactsRecalled    int     `json:"memory_facts_recalled,omitempty"`
	Compactions            int     `json:"compactions,omitempty"`
	CompactionsLevel3      int     `json:"compactions_level3,omitempty"`
	CompactionCostUSD      float64 `json:"compaction_cost_usd,omitempty"`

	// Cache is the prompt-cache telemetry of the session (misses, expiries,
	// rebuilds, hit share, write/read ratio), persisted so a later /cost
	// sessions can compare sessions instead of losing the counters with
	// the process. Nil on snapshots written before it existed.
	Cache *CacheTelemetrySnapshot `json:"cache,omitempty"`
}

// CostTracker tracks token usage and estimated cost for the current session,
// with per-model granularity, real API usage data support, cache token pricing,
// write-through session persistence, and configurable budget enforcement.
type CostTracker struct {
	// Provider context engine bookkeeping (server-side clears).
	contextEditsApplied  int
	contextEditsToolUses int
	contextEditsTokens   int64
	mu                   sync.RWMutex
	// logger receives the per-request prompt-cache observation (SetLogger);
	// nil logs nothing.
	logger *zap.Logger
	// onRealUsage runs after every provider-reported usage is booked, with
	// the tracker unlocked (SetRealUsageHook). The cache keep-alive
	// scheduler hangs off it: a request is the moment the cache entry's
	// timer restarts.
	onRealUsage func(provider, model string)

	// storeDir overrides the snapshot directory (per-tenant store sets);
	// empty means the process default (costStoreDir).
	storeDir string

	sessionID    string
	sessionName  string
	sessionStart time.Time
	lastUpdate   time.Time

	// Per-model usage: key is "provider:model"
	modelUsage map[string]*ModelUsageRecord

	// Aggregates (computed from modelUsage)
	// cacheTTLPromoted records that this session already asked for the
	// hour-long prompt cache, so the promotion happens once and is never
	// undone. See promoteCacheTTLIfIdling.
	cacheTTLPromoted      bool
	totalPromptTokens     int64
	totalInputTokens      int64
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
	// Daily budget (CHATCLI_DAILY_BUDGET_USD): spend across every session
	// of the calendar day under this store dir (the tenant root under the
	// gateway), persisted in daily-spend.json.
	dailyLimitUSD  float64
	dailySpentUSD  float64
	dailyDate      string
	dailyBaseline  float64 // totalCostUSD already folded into dailySpentUSD
	dailySaveTimer *time.Timer

	// lastAnnouncedLevel arms the one-shot proactive budget notice: a
	// transition is reported once per escalation, not on every turn.
	lastAnnouncedLevel BudgetLevel

	// Prompt-cache telemetry (provider-neutral, fed by RecordRealUsage).
	cache cacheTelemetry

	// Explicit cache resources: storage cost outside any usage record.
	cacheResources         int
	cacheStorageTokenHours float64
	cacheStorageUSD        float64
	compactions            int
	memoryCalls            int
	memoryCostUSD          float64
	// What the memory worker produced and what the conversation read back:
	// the two sides of the memory ROI (cost_compaction.go).
	memoryFactsWritten    int
	memoryEpisodesWritten int
	memoryRecalls         int
	memoryFactsRecalled   int
	embeddingCalls        int
	embeddingTokens       int64
	embeddingCostUSD      float64
	compactionsLevel3     int
	compactionCostUSD     float64

	// Persistence write-through throttle.
	lastSave time.Time

	// For backward compat display
	lastProvider string
	lastModel    string
}

// NewCostTracker creates a new cost tracker with optional budget limit.
func NewCostTracker() *CostTracker {
	return NewCostTrackerAt("")
}

// NewCostTrackerAt is NewCostTracker persisting snapshots under dir
// (empty = process default).
func NewCostTrackerAt(dir string) *CostTracker {
	ct := &CostTracker{
		storeDir:     dir,
		sessionID:    newCostSessionID(time.Now()),
		sessionStart: time.Now(),
		lastUpdate:   time.Now(),
		modelUsage:   make(map[string]*ModelUsageRecord),
	}
	ct.loadBudgetFromEnvLocked()
	ct.loadDailySpendLocked()
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

	ct.dailyLimitUSD = 0
	if v := os.Getenv("CHATCLI_DAILY_BUDGET_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			ct.dailyLimitUSD = f
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
// RecordContextEdits books what the provider context engine cleared
// server-side (tool results and the input tokens they held).
func (ct *CostTracker) RecordContextEdits(clearedToolUses, clearedInputTokens int) {
	if ct == nil {
		return
	}
	ct.mu.Lock()
	ct.contextEditsApplied++
	ct.contextEditsToolUses += clearedToolUses
	ct.contextEditsTokens += int64(clearedInputTokens)
	ct.mu.Unlock()
}

// ContextEditStats returns how many provider context edits were applied
// this session, the tool results cleared and the input tokens freed.
func (ct *CostTracker) ContextEditStats() (edits, toolUses int, tokens int64) {
	if ct == nil {
		return 0, 0, 0
	}
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.contextEditsApplied, ct.contextEditsToolUses, ct.contextEditsTokens
}

func (ct *CostTracker) RecordRealUsage(provider, model string, usage *models.UsageInfo) {
	ct.RecordRealUsageIn(LaneMain, provider, model, usage)
}

// UsageLane names the conversation a usage report belongs to. The cost
// is booked the same way whatever the lane; what differs is the prefix
// cache telemetry, which judges a request against the previous one of
// the same provider and therefore must only ever see one conversation.
// The memory worker's extraction prompt and a squad worker's own loop
// share the provider with the main conversation but not its prefix: fed
// into the same bucket they read as lost prefixes, inflate the write/read
// ratio, and disarm the keep-alive.
type UsageLane string

const (
	// LaneMain is the user's conversation: chat, agent and coder turns.
	LaneMain UsageLane = "main"
	// LaneBackground is ChatCLI's own housekeeping: memory extraction,
	// rollups, memory compaction, the compaction summarizer.
	LaneBackground UsageLane = "background"
	// LaneWorker is a squad, taskgraph or delegate worker's own loop.
	LaneWorker UsageLane = "worker"
)

// RecordRealUsageIn books one usage report under a lane. Every lane
// counts toward the session's tokens and dollars; only the main lane
// feeds the prefix-cache telemetry, the ttl promotion and the keep-alive
// schedule, because those describe the user's conversation and nothing
// else shares its prefix.
func (ct *CostTracker) RecordRealUsageIn(lane UsageLane, provider, model string, usage *models.UsageInfo) {
	if usage == nil {
		return
	}
	if lane == "" {
		lane = LaneMain
	}
	// Long-context tiers are per call (the record only knows totals): a
	// call past the provider's threshold is booked at its tier price as a
	// billed amount, so the table math never averages it away.
	if tiered := tieredCallCostUSD(provider, model, usage); tiered > 0 {
		u := *usage
		u.CostUSD = tiered
		usage = &u
	}
	ct.mu.Lock()

	key := modelKey(provider, model)
	rec := ct.getOrCreateRecord(key, provider, model)

	promptForRecord := promptTokensForRecord(provider, model, usage)
	rec.PromptTokens += int64(promptForRecord)
	rec.CompletionTokens += int64(usage.CompletionTokens)
	// InputTokens is the comparable figure across providers: PromptTokens
	// means "uncached delta" on additive schemas and "whole input" on
	// subset ones, so summing it alone made the /cost totals of a Bedrock
	// session and an OpenAI session incomparable. Cost still prices
	// PromptTokens and the cache pools separately — this is the display and
	// reporting number, not a billing input.
	inputTokens := contextTokens(provider, model, usage)
	rec.InputTokens += int64(inputTokens)
	totalTokens := inputTokens + usage.CompletionTokens
	if reported := usage.TotalTokens; reported > totalTokens {
		// Never shrink a provider-reported total that folds in counts of
		// its own (Gemini adds thoughtsTokenCount).
		totalTokens = reported
	}
	rec.TotalTokens += int64(totalTokens)
	rec.CacheCreationTokens += int64(usage.CacheCreationInputTokens)
	rec.CacheReadTokens += int64(usage.CacheReadInputTokens)
	rec.CacheCreation1hTokens += int64(usage.CacheCreation1hInputTokens)
	rec.ReasoningTokens += int64(usage.ReasoningTokens)
	if usage.IsReal && lane == LaneMain {
		ct.logCacheObservation(ct.cache.observe(provider, model, usage, time.Now()))
		ct.promoteCacheTTLIfIdling(provider, model)
	} else if usage.IsReal {
		ct.logLaneUsage(lane, provider, model, usage)
	}
	if usage.CostUSD > 0 {
		// This call's tokens are covered by the provider-billed amount —
		// remember them so recomputeCost prices only the uncovered rest.
		rec.ProviderCostUSD += usage.CostUSD
		rec.BilledPromptTokens += int64(promptForRecord)
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
	hook := ct.onRealUsage
	usageEvents := ct.pulseUsageEventsLocked(lane, rec, inputTokens, usage)
	ct.mu.Unlock()

	for _, ev := range usageEvents {
		pulse.Emit(ev)
	}
	if shouldSave {
		_ = ct.SaveSession()
	}
	if hook != nil && usage.IsReal && lane == LaneMain {
		hook(provider, model)
	}
}

// RecordUsage records tokens used for a single LLM request (legacy path).
func (ct *CostTracker) RecordUsage(provider, model string, promptTokens, completionTokens int) {
	ct.RecordRealUsage(provider, model, &models.UsageInfo{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
		InputTokensTotal: promptTokens,
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
	// Every aggregate the model map does not own: cache storage, memory
	// worker, embeddings, compaction, cache resources and provider context
	// edits — otherwise the total stays above zero and a hard stop armed
	// by the old session survives the reset.
	ct.cacheStorageUSD = 0
	ct.cacheResources = 0
	ct.memoryCostUSD = 0
	ct.memoryCalls = 0
	ct.embeddingCostUSD = 0
	ct.compactionCostUSD = 0
	ct.contextEditsApplied, ct.contextEditsToolUses, ct.contextEditsTokens = 0, 0, 0
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
	if !ct.budgetHardStop {
		return false
	}
	if ct.budgetLimitUSD > 0 && ct.totalCostUSD >= ct.budgetLimitUSD {
		return true
	}
	return ct.dailyLimitUSD > 0 && ct.dailySpentUSD >= ct.dailyLimitUSD
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
	return ct.totalInputTokens + ct.totalCompletionTokens
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
		TotalTokens:   ct.totalInputTokens + ct.totalCompletionTokens,
		Cache:         ct.cache.snapshot(ct.cacheTTLPromoted),

		CacheResources:         ct.cacheResources,
		CacheStorageTokenHours: ct.cacheStorageTokenHours,
		CacheStorageCostUSD:    ct.cacheStorageUSD,
		EmbeddingCalls:         ct.embeddingCalls,
		EmbeddingTokens:        ct.embeddingTokens,
		EmbeddingCostUSD:       ct.embeddingCostUSD,
		MemoryCalls:            ct.memoryCalls,
		MemoryCostUSD:          ct.memoryCostUSD,
		MemoryFactsWritten:     ct.memoryFactsWritten,
		MemoryEpisodesWritten:  ct.memoryEpisodesWritten,
		MemoryRecalls:          ct.memoryRecalls,
		MemoryFactsRecalled:    ct.memoryFactsRecalled,
		Compactions:            ct.compactions,
		CompactionsLevel3:      ct.compactionsLevel3,
		CompactionCostUSD:      ct.compactionCostUSD,
	}
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

	dir := ct.storeDir
	if dir == "" {
		dir = costStoreDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cost store dir: %w", err)
	}

	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cost session: %w", err)
	}
	if b, err = atrest.Seal(b); err != nil {
		return fmt.Errorf("seal cost session: %w", err)
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
	ct.cacheResources = data.CacheResources
	ct.cacheStorageTokenHours = data.CacheStorageTokenHours
	ct.cacheStorageUSD = data.CacheStorageCostUSD
	ct.embeddingCalls = data.EmbeddingCalls
	ct.embeddingTokens = data.EmbeddingTokens
	ct.embeddingCostUSD = data.EmbeddingCostUSD
	ct.memoryCalls = data.MemoryCalls
	ct.memoryCostUSD = data.MemoryCostUSD
	ct.memoryFactsWritten = data.MemoryFactsWritten
	ct.memoryEpisodesWritten = data.MemoryEpisodesWritten
	ct.memoryRecalls = data.MemoryRecalls
	ct.memoryFactsRecalled = data.MemoryFactsRecalled
	ct.compactions = data.Compactions
	ct.compactionsLevel3 = data.CompactionsLevel3
	ct.compactionCostUSD = data.CompactionCostUSD
	ct.cache.restore(data.Cache)
	if data.Cache != nil {
		ct.cacheTTLPromoted = data.Cache.TTLPromoted
	}
	ct.recomputeAggregates()
	return nil
}

// LoadCostSnapshot reads one persisted snapshot by session id.
func LoadCostSnapshot(sessionID string) (*SessionCostData, error) {
	path := filepath.Join(costStoreDir(), filepath.Base(sessionID)+".json")
	b, err := os.ReadFile(filepath.Clean(path))
	if err == nil {
		b, err = atrest.Open(b)
	}
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

func (ct *CostTracker) recomputeAggregates() {
	ct.totalPromptTokens = 0
	ct.totalInputTokens = 0
	ct.totalCompletionTokens = 0
	ct.totalCacheCreation = 0
	ct.totalCacheRead = 0
	ct.totalReasoning = 0
	ct.totalRequests = 0
	ct.totalCostUSD = 0

	for _, rec := range ct.modelUsage {
		ct.totalPromptTokens += rec.PromptTokens
		ct.totalInputTokens += recordInputTokens(rec)
		ct.totalCompletionTokens += rec.CompletionTokens
		ct.totalCacheCreation += rec.CacheCreationTokens
		ct.totalCacheRead += rec.CacheReadTokens
		ct.totalReasoning += rec.ReasoningTokens
		ct.totalRequests += rec.Requests
		ct.totalCostUSD += rec.TotalCostUSD
	}
	ct.totalCostUSD += ct.cacheStorageUSD
	ct.totalCostUSD += ct.embeddingCostUSD
	ct.accrueDailyLocked()
}

func (ct *CostTracker) budgetLevelLocked() BudgetLevel {
	level := BudgetOK
	if ct.budgetLimitUSD > 0 {
		switch {
		case ct.totalCostUSD >= ct.budgetLimitUSD:
			level = BudgetExceeded
		case ct.totalCostUSD >= ct.budgetLimitUSD*ct.budgetWarningPct:
			level = BudgetWarning
		}
	}
	if ct.dailyLimitUSD > 0 {
		switch {
		case ct.dailySpentUSD >= ct.dailyLimitUSD:
			level = BudgetExceeded
		case ct.dailySpentUSD >= ct.dailyLimitUSD*ct.budgetWarningPct && level == BudgetOK:
			level = BudgetWarning
		}
	}
	return level
}

func (ct *CostTracker) budgetMessageLocked() string {
	// The daily limit speaks for itself when it tripped (or when it is the
	// only limit configured); the session limit otherwise.
	if ct.dailyLimitUSD > 0 && (ct.budgetLimitUSD <= 0 || ct.dailySpentUSD >= ct.dailyLimitUSD*ct.budgetWarningPct) {
		dailyPct := ct.dailySpentUSD / ct.dailyLimitUSD * 100
		switch {
		case ct.dailySpentUSD >= ct.dailyLimitUSD:
			if ct.budgetHardStop {
				return i18n.T("cost.budget.daily_exceeded_hard", ct.dailySpentUSD, ct.dailyLimitUSD, dailyPct)
			}
			return i18n.T("cost.budget.daily_exceeded", ct.dailySpentUSD, ct.dailyLimitUSD, dailyPct)
		case ct.dailySpentUSD >= ct.dailyLimitUSD*ct.budgetWarningPct:
			return i18n.T("cost.budget.daily_warning", ct.dailySpentUSD, ct.dailyLimitUSD, dailyPct)
		}
	}
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

// Deprecated: GetSummary has no callers in ChatCLI; /cost renders the
// localized summary. Kept only because removing an exported method is an
// API break for embedders. Returns a compact provider/model/turns/cost line.
func (ct *CostTracker) GetSummary(provider, model string, history int) string {
	return fmt.Sprintf("%s/%s · %d · $%.4f", provider, model, history, ct.TotalCost())
}

// --- Pricing engine (llm/pricing) ---
//
// The USD pricing engine — per-model list prices, cache discounts, the
// schema rules and the record formula — lives in the leaf package
// llm/pricing so the gRPC server and the Kubernetes operator price usage
// exactly the way /cost does. The tracker keeps its historical unexported
// names as thin wrappers: every caller and test in this package reads the
// same, and the numbers are byte-identical because the arithmetic moved
// verbatim (TestRecomputeRecordCost_MatchesLegacyFormula pins that).

// reasoningTokensAdditive reports whether a provider's reasoning tokens are
// reported outside the completion count (Gemini) and must be billed on
// top of it.
func reasoningTokensAdditive(provider string) bool {
	return pricing.ReasoningTokensAdditive(provider)
}

// recordPricingInput is the record's token ledger in the engine's shape.
func recordPricingInput(rec *ModelUsageRecord) pricing.RecordInput {
	return pricing.RecordInput{
		Provider:                  rec.Provider,
		Model:                     rec.Model,
		PromptTokens:              rec.PromptTokens,
		CompletionTokens:          rec.CompletionTokens,
		ReasoningTokens:           rec.ReasoningTokens,
		CacheCreationTokens:       rec.CacheCreationTokens,
		CacheReadTokens:           rec.CacheReadTokens,
		CacheCreation1hTokens:     rec.CacheCreation1hTokens,
		ProviderCostUSD:           rec.ProviderCostUSD,
		BilledPromptTokens:        rec.BilledPromptTokens,
		BilledCompletionTokens:    rec.BilledCompletionTokens,
		BilledCacheReadTokens:     rec.BilledCacheReadTokens,
		BilledCacheCreationTokens: rec.BilledCacheCreationTokens,
	}
}

// recomputeRecordCost prices one record in place — the ONLY cost formula
// in the tracker (pricing.RecordCost); estimateTurnCostUSD delegates to
// the same engine so per-turn and per-session math can never disagree.
func recomputeRecordCost(rec *ModelUsageRecord) {
	c := pricing.RecordCost(recordPricingInput(rec))
	rec.PricingKnown = c.Known
	rec.InputCostUSD = c.InputUSD
	rec.OutputCostUSD = c.OutputUSD
	rec.CacheCostUSD = c.CacheUSD
	rec.TotalCostUSD = c.TotalUSD
}

// estimateTurnCostUSD prices a single turn's usage with the same rules the
// session tracker applies (cache semantics included), so the chat envelope
// footer and /cost never disagree about the same turn. A provider-reported
// cost wins outright.
func estimateTurnCostUSD(provider, model string, usage *models.UsageInfo) float64 {
	return pricing.CostOf(provider, model, usage).TotalUSD
}

// getModelPricing returns input and output cost per 1M tokens for known models.
// Prices in USD per 1M tokens.
func getModelPricing(provider, model string) (inputCost, outputCost float64) {
	in, out, _ := lookupModelPricing(provider, model)
	return in, out
}

// lookupModelPricing is getModelPricing plus a known flag: known=false means
// the model matched NO table entry — cost zero because the price is unknown,
// not because the backend is unmetered. Ollama/StackSpot/Copilot return
// known=true with zero prices (deliberately free from ChatCLI's viewpoint),
// and so does a Devin family no listing ever priced.
func lookupModelPricing(provider, model string) (inputCost, outputCost float64, known bool) {
	return pricing.ListPrice(provider, model)
}

// getCachePricing returns cache write and cache read cost per 1M tokens.
func getCachePricing(provider, model string) (cacheWriteCost, cacheReadCost float64) {
	return pricing.CacheRates(provider, model)
}

// cacheWrite1hCost is the per-1M price of a cache write with the 1-hour TTL.
func cacheWrite1hCost(provider, model string, write5m float64) float64 {
	return pricing.CacheWrite1hPerMTok(provider, model, write5m)
}

// cacheTokensAdditive reports whether the usage payload counts cache tokens
// ALONGSIDE the prompt count (Anthropic Messages schema) rather than as a
// subset of it (OpenAI cached_tokens, Gemini cachedContentTokenCount).
func cacheTokensAdditive(provider, model string) bool {
	return pricing.CacheTokensAdditive(provider, model)
}

// longContextMultipliers returns the input and output price multipliers a
// call with promptTokens of context pays (1/1 outside a long-context tier).
func longContextMultipliers(provider, model string, promptTokens int) (in, out float64) {
	return pricing.LongContextMultipliers(provider, model, promptTokens)
}

// tieredCallCostUSD prices one call with its long-context tier applied; 0
// when the call is not in a tier (the record math prices it normally).
func tieredCallCostUSD(provider, model string, usage *models.UsageInfo) float64 {
	return pricing.TieredCallCostUSD(provider, model, usage)
}

// promptTokensForRecord converts one call's prompt count into the
// convention recomputeRecordCost prices the record's PromptTokens under.
func promptTokensForRecord(provider, model string, usage *models.UsageInfo) int {
	return pricing.PromptTokensForRecord(provider, model, usage)
}

// contextTokens is the input the model actually held for the turn — what
// the ctx% figure must measure against the window.
func contextTokens(provider, model string, usage *models.UsageInfo) int {
	return pricing.ContextTokens(provider, model, usage)
}

// recordInputTokens is the schema-normalized input of a record, with the
// fallback that keeps sessions persisted before normalization readable:
// their InputTokens is zero, and PromptTokens is what those builds recorded.
func recordInputTokens(rec *ModelUsageRecord) int64 {
	if rec == nil {
		return 0
	}
	if rec.InputTokens > 0 {
		return rec.InputTokens
	}
	return rec.PromptTokens
}

// sessionIDSnapshot returns the current cost-session id (reset by /cost
// reset); the audit trail keys its lines by it.
func (ct *CostTracker) sessionIDSnapshot() string {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.sessionID
}
