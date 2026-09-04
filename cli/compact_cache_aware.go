/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import "github.com/diillson/chatcli/models"

// Cache-aware compaction.
//
// Compaction rewrites the history, which moves every byte after the point
// it edits and forces the provider to write the whole prefix into its
// cache again. Deciding purely on size means a session can pay for that
// rewrite one turn after paying to warm the cache, and then pay to warm
// it again — the write costs more than the read it replaced.
//
// So a compaction that is merely *advisable* waits while the prefix is
// still warm: the turn runs against the cache it already paid for, and the
// pass happens once the cache has gone cold anyway. A compaction that is
// *necessary* never waits, because overflowing the window is worse than
// any cache write.

// hardCeilingRatio is how far past the budget the history may drift while
// a compaction is being deferred. Beyond it the pass runs regardless of
// the cache: the margin exists to absorb one or two turns, not a run.
const hardCeilingRatio = 1.15

// deferCompactionForWarmCache reports whether this turn should skip an
// otherwise-due compaction because the provider's prefix cache is still
// warm and the history is only modestly over budget.
//
// Returns false whenever the answer is not clearly yes: no tracker, no
// cache activity yet, a provider whose cache the session cannot observe,
// or a history already past the hard ceiling.
func (cli *ChatCLI) deferCompactionForWarmCache(history []models.Message, cfg CompactConfig) bool {
	if cli == nil || cli.costTracker == nil || cli.historyCompactor == nil {
		return false
	}
	stats := cli.costTracker.CacheStats()
	// No request has reported cache tokens yet, so there is no warm prefix
	// to protect and nothing to weigh against the rewrite.
	if !stats.Warm || stats.Requests == 0 {
		return false
	}
	budget := cli.historyCompactor.CharBudget(cfg)
	if budget <= 0 {
		return false
	}
	ceiling := int(float64(budget) * hardCeilingRatio)
	return totalChars(history) <= ceiling
}
