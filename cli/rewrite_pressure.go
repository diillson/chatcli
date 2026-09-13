/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * When the cheap in-place history rewrites are worth their cache cost.
 *
 * Microcompact, repeated-read dedup and skill aging all edit messages that
 * sit ahead of the provider's rolling cache breakpoint. On every provider
 * with a prefix cache, an edited message rewrites the prefix from that
 * point onward: the whole tail is billed as a cache write (1.25x or 2x the
 * input price on Anthropic and Bedrock, full price instead of the cached
 * discount on OpenAI, Gemini, xAI, Kimi and the rest) instead of a read.
 * Those passes used to run every turn by message age, which made the
 * rewrite the rule: shaving a few hundred tokens off an old tool result
 * cost re-caching everything after it. A rewrite only pays for itself when
 * the window is actually filling up, or when there is no warm prefix to
 * throw away.
 */
package cli

import (
	"os"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/models"
)

// rewritePressureRatio is the share of the compaction budget the history
// must reach before the age-based rewrites run against a warm prefix
// cache. Below it the passes wait; the tokens they would save are cheaper
// to keep reading from cache than to rewrite. At it the history is close
// to the compaction threshold, and shrinking it now is what keeps the far
// more expensive summarizing compaction away.
const rewritePressureRatio = 0.75

// HistoryRewritePressureEnv overrides rewritePressureRatio: a share of the
// compaction budget between 0 and 1. "0" restores the previous behavior
// (rewrite every turn by message age, whatever the cache), "1" waits until
// the history reaches the budget itself. Anything unparsable or out of
// range keeps the default.
const HistoryRewritePressureEnv = "CHATCLI_HISTORY_REWRITE_PRESSURE"

// historyRewritePressure returns the pressure ratio in effect, read live so
// a /config change applies on the next turn.
func historyRewritePressure() float64 {
	raw := strings.TrimSpace(os.Getenv(HistoryRewritePressureEnv))
	if raw == "" {
		return rewritePressureRatio
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 || v > 1 {
		return rewritePressureRatio
	}
	return v
}

// historyRewriteAllowed reports whether this turn should run the in-place
// history rewrites (microcompact, read dedup, skill aging).
//
// True whenever there is no warm prefix cache to protect: no tracker, no
// request that reported cache tokens yet, a provider whose cache the
// session cannot observe, or a cache that has gone cold — the prefix is
// being written again anyway, so a rewrite costs nothing extra. Against a
// warm cache the passes wait until the history reaches the pressure
// ratio of the compaction budget; a ratio of 0 (HistoryRewritePressureEnv)
// runs them every turn as before.
func (cli *ChatCLI) historyRewriteAllowed(history []models.Message, cfg CompactConfig) bool {
	if cli == nil || cli.costTracker == nil || cli.historyCompactor == nil {
		return true
	}
	ratio := historyRewritePressure()
	if ratio <= 0 {
		return true
	}
	stats := cli.costTracker.CacheStats()
	if !stats.Warm || stats.Requests == 0 {
		return true
	}
	budget := cli.historyCompactor.CharBudget(cfg)
	if budget <= 0 {
		return true
	}
	return totalChars(history) >= int(float64(budget)*ratio)
}
