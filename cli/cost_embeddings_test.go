/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import "testing"

func TestRecordEmbeddingUsage_PricesAndPersists(t *testing.T) {
	ct := NewCostTracker()
	ct.RecordEmbeddingUsage("openai:text-embedding-3-small", 4_000_000) // ~1M tokens → $0.02
	ct.RecordEmbeddingUsage("ollama:nomic-embed-text", 4_000_000)       // free
	calls, tokens, cost := ct.EmbeddingStats()
	if calls != 2 || tokens < 2_000_000 || cost < 0.019 || cost > 0.021 {
		t.Fatalf("stats = %d %d %f", calls, tokens, cost)
	}
	snap := ct.Snapshot()
	if snap.EmbeddingCalls != 2 || snap.EmbeddingCostUSD != cost || snap.TotalCostUSD < cost {
		t.Fatalf("snapshot = %+v", snap)
	}
	var nilTracker *CostTracker
	nilTracker.RecordEmbeddingUsage("x", 10)
	ct.RecordEmbeddingUsage("x", 0)
}
