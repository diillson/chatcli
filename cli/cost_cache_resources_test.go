/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"math"
	"testing"
	"time"

	llmclient "github.com/diillson/chatcli/llm/client"
)

func TestCacheStoragePerMTokenHour(t *testing.T) {
	cases := map[string]float64{
		"gemini-2.5-flash":      1.00,
		"gemini-2.5-pro":        4.50,
		"gemini-3.1-pro":        4.50,
		"gemini-3.5-flash":      1.00,
		"gemini-3.5-flash-lite": 1.00,
		"gemini-3.8-flash":      0.50,
	}
	for model, want := range cases {
		if got := cacheStoragePerMTokenHour("GOOGLEAI", model); got != want {
			t.Fatalf("%s: storage rate %v, want %v", model, got, want)
		}
	}
	if got := cacheStoragePerMTokenHour("CLAUDEAI", "claude-sonnet-5"); got != 0 {
		t.Fatalf("implicit-cache providers bill no storage, got %v", got)
	}
}

func TestRecordCacheStorage_PricesGrantedLifetimeAndPersistsInSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	ct.RecordUsage("GOOGLEAI", "gemini-2.5-flash", 10, 1) // makes the snapshot persistable
	base := ct.TotalCost()

	ct.RecordCacheStorage(llmclient.CacheResourceEvent{Provider: "GOOGLEAI", Model: "gemini-2.5-flash",
		Action: llmclient.CacheResourceCreated, Tokens: 100_000, TTL: time.Hour})
	// 100k tokens × $1.00/M/h × 1h = $0.10
	if d := ct.TotalCost() - base; math.Abs(d-0.10) > 1e-9 {
		t.Fatalf("storage cost = %v, want 0.10", d)
	}
	ct.RecordCacheStorage(llmclient.CacheResourceEvent{Provider: "GOOGLEAI", Model: "gemini-2.5-flash",
		Action: llmclient.CacheResourceRefreshed, Tokens: 100_000, TTL: 30 * time.Minute})
	if d := ct.TotalCost() - base; math.Abs(d-0.15) > 1e-9 {
		t.Fatalf("refresh must add the extension, got %v", d)
	}
	ct.RecordCacheStorage(llmclient.CacheResourceEvent{Provider: "GOOGLEAI", Model: "gemini-2.5-flash",
		Action: llmclient.CacheResourceReleased, Tokens: 100_000})
	if d := ct.TotalCost() - base; math.Abs(d-0.15) > 1e-9 {
		t.Fatalf("release must not refund, got %v", d)
	}

	snap := ct.Snapshot()
	if snap.CacheResources != 1 || math.Abs(snap.CacheStorageCostUSD-0.15) > 1e-9 {
		t.Fatalf("snapshot must carry storage cost, got %+v", snap)
	}
	if err := ct.SaveSession(); err != nil {
		t.Fatal(err)
	}
	restored := NewCostTracker()
	if err := restored.RestoreSession(snap.SessionID); err != nil {
		t.Fatal(err)
	}
	if math.Abs(restored.TotalCost()-ct.TotalCost()) > 1e-9 {
		t.Fatalf("restored total %v != %v", restored.TotalCost(), ct.TotalCost())
	}
}

func TestGeminiCacheReadPricing_TenPercent(t *testing.T) {
	inputCost, _, _ := lookupModelPricing("GOOGLEAI", "gemini-2.5-flash")
	_, read := getCachePricing("GOOGLEAI", "gemini-2.5-flash")
	if inputCost <= 0 || math.Abs(read-inputCost*0.10) > 1e-9 {
		t.Fatalf("gemini cached reads must be 10%% of input: input=%v read=%v", inputCost, read)
	}
}
