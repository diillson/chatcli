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

import (
	"fmt"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

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

// RecordMemoryUsage accounts one background memory-worker call
// (extraction, rollup, memory compaction): the usage joins the totals
// like any request and the memory slice is kept apart for /cost.
func (ct *CostTracker) RecordMemoryUsage(provider, model string, usage *models.UsageInfo) {
	if ct == nil || usage == nil {
		return
	}
	cost := estimateTurnCostUSD(provider, model, usage)
	ct.RecordRealUsage(provider, model, usage)
	ct.mu.Lock()
	ct.memoryCalls++
	ct.memoryCostUSD += cost
	ct.mu.Unlock()
}

// RecordMemoryOutcome counts what one extraction persisted.
func (ct *CostTracker) RecordMemoryOutcome(facts, episodes int) {
	if ct == nil {
		return
	}
	ct.mu.Lock()
	ct.memoryFactsWritten += facts
	ct.memoryEpisodesWritten += episodes
	ct.mu.Unlock()
}

// RecordMemoryRecall counts one proactive recall that put facts in front
// of the model: the read side of the memory's return on investment.
func (ct *CostTracker) RecordMemoryRecall(facts int) {
	if ct == nil || facts <= 0 {
		return
	}
	ct.mu.Lock()
	ct.memoryRecalls++
	ct.memoryFactsRecalled += facts
	ct.mu.Unlock()
}

// MemoryROI is the memory worker's balance for /cost: what it cost,
// what it wrote, and how much of the store the conversation read back.
type MemoryROI struct {
	Calls           int
	CostUSD         float64
	FactsWritten    int
	EpisodesWritten int
	Recalls         int
	FactsRecalled   int
}

// CostPerFact is the extraction spend per persisted fact or episode; 0
// when nothing was written.
func (r MemoryROI) CostPerFact() float64 {
	n := r.FactsWritten + r.EpisodesWritten
	if n == 0 {
		return 0
	}
	return r.CostUSD / float64(n)
}

// MemoryROIStats returns the session's memory balance.
func (ct *CostTracker) MemoryROIStats() MemoryROI {
	if ct == nil {
		return MemoryROI{}
	}
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return MemoryROI{
		Calls: ct.memoryCalls, CostUSD: ct.memoryCostUSD,
		FactsWritten: ct.memoryFactsWritten, EpisodesWritten: ct.memoryEpisodesWritten,
		Recalls: ct.memoryRecalls, FactsRecalled: ct.memoryFactsRecalled,
	}
}

// MemoryStats returns the session's background memory-worker counters.
func (ct *CostTracker) MemoryStats() (calls int, costUSD float64) {
	if ct == nil {
		return 0, 0
	}
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.memoryCalls, ct.memoryCostUSD
}

// printBackgroundCostLines renders the /cost lines for spend that no
// interactive turn owns: the memory worker and the compaction summarizer
// (caller holds ct.mu read lock).
func printBackgroundCostLines(p string, ct *CostTracker) {
	if ct.memoryCalls > 0 {
		fmt.Println(p + "    " + colorize(i18n.T("cost.cmd.memory_worker", ct.memoryCalls, fmt.Sprintf("$%.4f", ct.memoryCostUSD)), ColorGray))
		fmt.Println(p + "    " + colorize(memoryROILine(MemoryROI{
			Calls: ct.memoryCalls, CostUSD: ct.memoryCostUSD,
			FactsWritten: ct.memoryFactsWritten, EpisodesWritten: ct.memoryEpisodesWritten,
			Recalls: ct.memoryRecalls, FactsRecalled: ct.memoryFactsRecalled,
		}), ColorGray))
	}
	if ct.compactions > 0 {
		fmt.Println(p + "    " + colorize(i18n.T("cost.cmd.compactions",
			ct.compactions, ct.compactionsLevel3, fmt.Sprintf("$%.4f", ct.compactionCostUSD)), ColorGray))
	}
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

// memoryROILine renders the memory balance under the worker's cost line:
// what the calls wrote, what it cost per item, and what the conversation
// read back through proactive recall. A worker that writes nothing the
// session ever reads is the case this line exists to expose.
func memoryROILine(r MemoryROI) string {
	perFact := "—"
	if v := r.CostPerFact(); v > 0 {
		perFact = fmt.Sprintf("$%.4f", v)
	}
	return i18n.T("cost.cmd.memory_roi", r.FactsWritten, r.EpisodesWritten, perFact, r.Recalls, r.FactsRecalled)
}
