/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Compaction accounting: how many times the history was compacted this
 * session, how many landed in Level 3, and what the Level 2 summarizer
 * cost. The summarizer's request is a real request on the session (or
 * the configured summarizer) route, so its usage joins the totals like
 * any turn; the compaction slice is kept apart so /cost can show it.
 */
package cli

// RecordCompaction accounts one Compact run from its report.
func (ct *CostTracker) RecordCompaction(rep CompactReport) {
	if ct == nil || rep.Level == 0 {
		return
	}
	var cost float64
	if rep.SummaryUsage != nil {
		cost = estimateTurnCostUSD(rep.SummaryProvider, rep.SummaryModel, rep.SummaryUsage)
		ct.RecordRealUsage(rep.SummaryProvider, rep.SummaryModel, rep.SummaryUsage)
	}
	ct.mu.Lock()
	ct.compactions++
	if rep.Level == 3 {
		ct.compactionsLevel3++
	}
	ct.compactionCostUSD += cost
	ct.mu.Unlock()
}

// CompactionStats returns the session's compaction counters.
func (ct *CostTracker) CompactionStats() (total, level3 int, costUSD float64) {
	if ct == nil {
		return 0, 0, 0
	}
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.compactions, ct.compactionsLevel3, ct.compactionCostUSD
}
