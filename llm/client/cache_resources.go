/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Explicit cache resources.
 *
 * Most providers cache prompt prefixes implicitly (Anthropic markers,
 * OpenAI/Moonshot/DeepSeek automatic prefix matching) — nothing to manage,
 * nothing to pay for beyond the discounted read. A few expose the cache as
 * a first-class RESOURCE the caller creates, references and pays storage
 * for while it lives (Gemini cachedContents). Those resources are opt-in
 * through CHATCLI_PROMPT_CACHE_EXPLICIT because they carry a cost that is
 * not visible in the per-request usage: storage per token-hour.
 *
 * This file holds what the adapters and the CLI share: the opt-in switch,
 * the configured lifetime, the observer through which an adapter reports
 * every resource it creates/refreshes/releases (the cost tracker prices
 * storage from these events), and the releaser registry the CLI drains at
 * shutdown so no resource outlives the session that paid for it.
 */
package client

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// PromptCacheExplicitEnv opts into provider cache RESOURCES (today: Gemini
// cachedContents). Off by default: explicit caches bill storage per
// token-hour on top of the discounted reads, so the operator decides.
const PromptCacheExplicitEnv = "CHATCLI_PROMPT_CACHE_EXPLICIT"

// ExplicitCacheEnabled reports whether adapters may create cache resources.
func ExplicitCacheEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(PromptCacheExplicitEnv))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// PromptCacheTTLDuration is the configured prompt-cache lifetime as a
// duration (the same setting AnthropicCacheTTL renders for the header).
func PromptCacheTTLDuration() time.Duration {
	if AnthropicCacheTTL() == "1h" {
		return time.Hour
	}
	return 5 * time.Minute
}

// CacheResourceAction is what happened to a cache resource.
type CacheResourceAction string

const (
	// CacheResourceCreated — a resource was created; Tokens and TTL are set.
	CacheResourceCreated CacheResourceAction = "created"
	// CacheResourceRefreshed — the lifetime was extended by TTL.
	CacheResourceRefreshed CacheResourceAction = "refreshed"
	// CacheResourceReleased — the resource was deleted before expiry.
	CacheResourceReleased CacheResourceAction = "released"
	// CacheResourceFailed — creation or refresh failed; Err carries why.
	CacheResourceFailed CacheResourceAction = "failed"
)

// CacheResourceEvent is one lifecycle event of a provider cache resource.
type CacheResourceEvent struct {
	Time     time.Time
	Provider string
	Model    string
	Name     string // provider handle (e.g. cachedContents/abc)
	Action   CacheResourceAction
	Tokens   int           // tokens the provider reports stored
	TTL      time.Duration // lifetime granted by this action
	Err      string
}

// CacheResourceObserver receives every cache resource event.
type CacheResourceObserver func(CacheResourceEvent)

var (
	cacheObsMu   sync.RWMutex
	cacheObsSink CacheResourceObserver

	cacheRelMu sync.Mutex
	cacheRel   = map[string]func(context.Context){}
)

// RegisterCacheResourceObserver installs (or clears, with nil) the process
// observer for cache resource events.
func RegisterCacheResourceObserver(fn CacheResourceObserver) {
	cacheObsMu.Lock()
	cacheObsSink = fn
	cacheObsMu.Unlock()
}

// EmitCacheResource reports one event to the registered observer. Adapters
// call it; the time is stamped here when the event carries none.
func EmitCacheResource(ev CacheResourceEvent) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	cacheObsMu.RLock()
	fn := cacheObsSink
	cacheObsMu.RUnlock()
	if fn != nil {
		fn(ev)
	}
}

// RegisterCacheReleaser records how to release a live cache resource. key
// identifies the owner (adapter instance); registering again replaces it.
func RegisterCacheReleaser(key string, release func(context.Context)) {
	if key == "" || release == nil {
		return
	}
	cacheRelMu.Lock()
	cacheRel[key] = release
	cacheRelMu.Unlock()
}

// UnregisterCacheReleaser forgets a releaser (the resource is gone).
func UnregisterCacheReleaser(key string) {
	cacheRelMu.Lock()
	delete(cacheRel, key)
	cacheRelMu.Unlock()
}

// ReleaseCacheResources runs every registered releaser once, in key order,
// and clears the registry. The CLI calls it at shutdown so paid storage
// stops with the session; each releaser is best-effort and bounded by ctx.
func ReleaseCacheResources(ctx context.Context) int {
	cacheRelMu.Lock()
	keys := make([]string, 0, len(cacheRel))
	for k := range cacheRel {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fns := make([]func(context.Context), 0, len(keys))
	for _, k := range keys {
		fns = append(fns, cacheRel[k])
	}
	cacheRel = map[string]func(context.Context){}
	cacheRelMu.Unlock()
	for _, fn := range fns {
		fn(ctx)
	}
	return len(fns)
}
