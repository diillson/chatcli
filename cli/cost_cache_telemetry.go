/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Provider-neutral prompt-cache telemetry: how much of the session's input
 * was served from cache, how many requests missed it, whether a miss was a
 * rebuild ChatCLI itself caused (compaction, microcompact, skill aging) and
 * whether the cached prefix is still warm. Fed by the cost tracker from the
 * cache fields every provider reports (Anthropic/Bedrock additive counts,
 * OpenAI/Gemini/Grok/Kimi subset counts), so it works on every provider
 * that reports cache tokens and stays silent on the ones that do not.
 */
package cli

import (
	"fmt"
	"strings"
	"time"

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

const (
	// cacheMissMinTokens and cacheMissMinShare define a miss the way an
	// operator would read it: a request re-processed (wrote back) a
	// meaningful chunk of what it could have read from cache. Tiny writes
	// (the new tail of every turn) are normal, not misses.
	cacheMissMinTokens = 2000
	cacheMissMinShare  = 0.05
	// cacheMissStreakAlert is how many consecutive misses trigger the
	// one-shot "your stable prefix is changing every turn" notice.
	cacheMissStreakAlert = 3
)

// cacheTelemetry is the tracker-side accumulator. All methods are called
// with the tracker's mutex held.
type cacheTelemetry struct {
	requests     int   // requests that reported any cache field
	misses       int   // re-processed content the cache already held
	rebuilds     int   // misses explained by a history rewrite we made
	readTokens   int64 // served from cache
	writeTokens  int64 // written to cache (additive schemas only)
	inputTokens  int64 // uncached input on additive schemas; full prompt on subset schemas
	lastActivity time.Time
	lastProvider string
	lastModel    string

	rebuildPending bool // set by NoteExpectedCacheRebuild until the next request
	missStreak     int
	alertArmed     bool // a streak notice is waiting to be shown
}

// observe folds one real usage report into the telemetry.
func (c *cacheTelemetry) observe(provider, model string, u *models.UsageInfo, now time.Time) {
	read := int64(u.CacheReadInputTokens)
	write := int64(u.CacheCreationInputTokens)
	prompt := int64(u.PromptTokens)
	additive := cacheTokensAdditive(provider, model)

	if read == 0 && write == 0 {
		// Provider reported no cache activity: nothing to learn about the
		// cache from this request (and no basis to call it a miss).
		return
	}
	first := c.requests == 0
	c.requests++
	c.readTokens += read
	c.lastActivity = now
	c.lastProvider = provider
	c.lastModel = model

	var miss bool
	if additive {
		c.writeTokens += write
		c.inputTokens += prompt
		// Anthropic/Bedrock: the write is the part of the prefix the cache
		// did not hold. A large write after the first request means the
		// prefix changed (or expired).
		miss = !first && write >= cacheMissMinTokens && float64(write) > cacheMissMinShare*float64(read+write)
	} else {
		c.inputTokens += prompt
		// OpenAI/Gemini/Grok/Kimi: cached tokens are a subset of the prompt.
		// A sizeable prompt served with (almost) nothing from cache after
		// earlier hits is a miss.
		miss = !first && prompt >= cacheMissMinTokens && float64(read) < cacheMissMinShare*float64(prompt)
	}
	switch {
	case miss && c.rebuildPending:
		c.rebuilds++
		c.rebuildPending = false
		c.missStreak = 0
	case miss:
		c.misses++
		c.missStreak++
		if c.missStreak == cacheMissStreakAlert {
			c.alertArmed = true
		}
	default:
		c.missStreak = 0
		c.rebuildPending = false
	}
}

// CacheStats is the read model /cost and the envelope footer render.
type CacheStats struct {
	Requests     int
	Misses       int
	Rebuilds     int
	HitPct       float64 // share of input served from cache, 0-100
	LastActivity time.Time
	TTL          string // "5m" or "1h"
	Warm         bool   // last activity within the TTL
}

// Reported is true when at least one request carried cache fields.
func (s CacheStats) Reported() bool { return s.Requests > 0 }

// cacheHitPct computes the share of input tokens served from cache for one
// usage report or an aggregate, honoring the reporting schema.
func cacheHitPct(additive bool, read, write, prompt int64) float64 {
	var denom int64
	if additive {
		denom = read + write + prompt
	} else {
		denom = prompt
	}
	if denom <= 0 || read <= 0 {
		return 0
	}
	pct := float64(read) / float64(denom) * 100
	if pct > 100 {
		pct = 100
	}
	return pct
}

// TurnCacheHitPct is the per-turn figure for the envelope footer. Returns
// ok=false when the usage carries no cache fields.
func TurnCacheHitPct(provider, model string, u *models.UsageInfo) (float64, bool) {
	if u == nil || (u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0) {
		return 0, false
	}
	return cacheHitPct(cacheTokensAdditive(provider, model),
		int64(u.CacheReadInputTokens), int64(u.CacheCreationInputTokens), int64(u.PromptTokens)), true
}

// cacheTTLFor names the cache lifetime in effect for a provider/model:
// the configured Anthropic TTL for the Anthropic family (direct or
// Bedrock), the providers' documented short window otherwise.
func cacheTTLFor(provider, model string) string {
	p := strings.ToLower(provider)
	m := strings.ToLower(model)
	if strings.Contains(p, "claudeai") || strings.Contains(m, "claude") || strings.Contains(m, "fable") {
		if strings.Contains(p, "bedrock") {
			return "5m"
		}
		return llmclient.AnthropicCacheTTL()
	}
	if (strings.Contains(p, "googleai") || strings.Contains(m, "gemini")) && llmclient.ExplicitCacheEnabled() {
		// Explicit cachedContents live for the configured lifetime.
		return llmclient.AnthropicCacheTTL()
	}
	return "5m"
}

func cacheTTLDuration(ttl string) time.Duration {
	if ttl == "1h" {
		return time.Hour
	}
	return 5 * time.Minute
}

// CacheStats returns the session's prompt-cache telemetry.
func (ct *CostTracker) CacheStats() CacheStats {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.cacheStatsLocked()
}

// formatIdle renders an idle duration compactly for the /cost cache line.
func formatIdle(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// cacheStatsLocked is CacheStats for callers already holding ct.mu.
func (ct *CostTracker) cacheStatsLocked() CacheStats {
	c := ct.cache
	stats := CacheStats{
		Requests:     c.requests,
		Misses:       c.misses,
		Rebuilds:     c.rebuilds,
		LastActivity: c.lastActivity,
	}
	if c.requests == 0 {
		return stats
	}
	additive := cacheTokensAdditive(c.lastProvider, c.lastModel)
	stats.HitPct = cacheHitPct(additive, c.readTokens, c.writeTokens, c.inputTokens)
	stats.TTL = cacheTTLFor(c.lastProvider, c.lastModel)
	stats.Warm = time.Since(c.lastActivity) < cacheTTLDuration(stats.TTL)
	return stats
}

// NoteExpectedCacheRebuild tells the telemetry that ChatCLI just rewrote
// the conversation (compaction, microcompact, skill aging, guided /compact),
// so the next request's cache write is an expected rebuild rather than a
// miss caused by an unstable prefix.
func (ct *CostTracker) NoteExpectedCacheRebuild() {
	if ct == nil {
		return
	}
	ct.mu.Lock()
	ct.cache.rebuildPending = true
	ct.mu.Unlock()
}

// TakeCacheMissAlert returns true once when a miss streak reached the
// alert threshold; the caller prints the one-shot notice.
func (ct *CostTracker) TakeCacheMissAlert() bool {
	if ct == nil {
		return false
	}
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if !ct.cache.alertArmed {
		return false
	}
	ct.cache.alertArmed = false
	return true
}
