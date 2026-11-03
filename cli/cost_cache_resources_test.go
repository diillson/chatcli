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
	}
	for model, want := range cases {
		if got := cacheStoragePerMTokenHour("GOOGLEAI", model); got != want {
			t.Fatalf("%s: storage rate %v, want %v", model, got, want)
		}
	}
	// 3.6/3.7/3.8 Flash storage doubles with their token price: $0.50
	// through 2026, $1.00 from Jan 1 2027 (midnight Pacific).
	pt := time.FixedZone("PST", -8*60*60)
	last := time.Date(2026, time.December, 31, 23, 59, 0, 0, pt)
	first := time.Date(2027, time.January, 1, 0, 0, 0, 0, pt)
	for _, model := range []string{"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash"} {
		if got := cacheStoragePerMTokenHourAt("GOOGLEAI", model, last); got != 0.50 {
			t.Fatalf("%s on %s: storage %v, want 0.50", model, last, got)
		}
		if got := cacheStoragePerMTokenHourAt("GOOGLEAI", model, first); got != 1.00 {
			t.Fatalf("%s on %s: storage %v, want 1.00", model, first, got)
		}
	}
	if got := cacheStoragePerMTokenHour("CLAUDEAI", "claude-sonnet-5"); got != 0 {
		t.Fatalf("implicit-cache providers bill no storage, got %v", got)
	}
}

func TestRecordCacheStorage_PricesGrantedLifetimeAndPersistsInSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ct := NewCostTracker()
	t.Cleanup(ct.FlushDailySpend)                         // the debounced daily-spend timer must not write during TempDir cleanup
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
