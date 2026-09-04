/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Embedding cost accounting: every Embed call of the session's provider
 * (knowledge retrieval, memory vectors, HyDE, warm-ups) is counted and
 * priced from the provider's list rate. Token counts are chars/4
 * estimates (embedding APIs rarely report usage), marked as such in the
 * /cost output; local providers (Ollama) cost nothing.
 */
package cli

import (
	"github.com/diillson/chatcli/llm/embedding"
)

// RecordEmbeddingUsage accounts one Embed call: chars of input, priced at
// the provider's rate per million tokens (chars/4).
func (ct *CostTracker) RecordEmbeddingUsage(provider string, chars int) {
	if ct == nil || chars <= 0 {
		return
	}
	tokens := int64(chars/4 + 1)
	cost := float64(tokens) / 1_000_000 * embedding.PricePerMillionTokens(provider)
	ct.mu.Lock()
	ct.embeddingCalls++
	ct.embeddingTokens += tokens
	ct.embeddingCostUSD += cost
	ct.totalCostUSD += cost
	// Embedding spend counts against the daily budget like any other.
	ct.accrueDailyLocked()
	ct.mu.Unlock()
}

// EmbeddingStats returns the session's embedding counters.
func (ct *CostTracker) EmbeddingStats() (calls int, tokens int64, costUSD float64) {
	if ct == nil {
		return 0, 0, 0
	}
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.embeddingCalls, ct.embeddingTokens, ct.embeddingCostUSD
}

// installEmbeddingUsageObserver routes every Embed call to this CLI's
// cost tracker (the tracker is tenant-scoped under the gateway, so the
// closure reads it at call time).
func (cli *ChatCLI) installEmbeddingUsageObserver() {
	embedding.SetUsageObserver(func(provider string, _ int, chars int, err error) {
		if err != nil || cli == nil {
			return
		}
		cli.costTracker.RecordEmbeddingUsage(provider, chars)
	})
}
