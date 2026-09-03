/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Storage pricing for explicit cache resources. Providers that expose the
 * cache as a resource (Gemini cachedContents) bill storage per token-hour
 * on top of the discounted reads, and that charge never appears in a
 * response's usage block — the only place it can be priced is from the
 * resource lifecycle events the adapters emit (client.EmitCacheResource).
 * The observer installed here routes them to whichever cost tracker is
 * active (the tenant swap may replace it between turns).
 */
package cli

import (
	"strings"

	llmclient "github.com/diillson/chatcli/llm/client"
)

// cacheStoragePerMTokenHour returns the storage price in USD per million
// tokens per hour for a provider/model, 0 when the provider does not bill
// storage (implicit caches). Google prices (ai.google.dev/gemini-api/docs/
// pricing, Sep/2026): 3.x Flash $0.50 (through 2026), Flash / Flash-Lite
// $1.00, Pro $4.50.
func cacheStoragePerMTokenHour(provider, model string) float64 {
	p := strings.ToLower(provider)
	m := strings.ToLower(model)
	if !strings.Contains(p, "googleai") && !strings.Contains(m, "gemini") {
		return 0
	}
	switch {
	case strings.Contains(m, "pro"):
		return 4.50
	case strings.Contains(m, "lite"):
		return 1.00
	case strings.HasPrefix(m, "gemini-3.") && strings.Contains(m, "flash") && !strings.HasPrefix(m, "gemini-3.5"):
		return 0.50
	default:
		return 1.00
	}
}

// initCacheResourceCosting installs the process observer that prices
// explicit cache storage into the active cost tracker.
func (cli *ChatCLI) initCacheResourceCosting() {
	llmclient.RegisterCacheResourceObserver(func(ev llmclient.CacheResourceEvent) {
		if cli == nil || cli.costTracker == nil {
			return
		}
		cli.costTracker.RecordCacheStorage(ev)
	})
}

// RecordCacheStorage prices one cache resource event: created and
// refreshed events buy TTL worth of storage for the resource's tokens;
// released and failed events change nothing (storage already paid for
// the granted lifetime is not refunded by the provider).
func (ct *CostTracker) RecordCacheStorage(ev llmclient.CacheResourceEvent) {
	if ev.Action != llmclient.CacheResourceCreated && ev.Action != llmclient.CacheResourceRefreshed {
		return
	}
	rate := cacheStoragePerMTokenHour(ev.Provider, ev.Model)
	cost := float64(ev.Tokens) / 1_000_000 * rate * ev.TTL.Hours()
	ct.mu.Lock()
	if ev.Action == llmclient.CacheResourceCreated {
		ct.cacheResources++
	}
	ct.cacheStorageTokenHours += float64(ev.Tokens) * ev.TTL.Hours()
	ct.cacheStorageUSD += cost
	ct.totalCostUSD += cost
	ct.mu.Unlock()
	_ = ct.SaveSession()
}
