/*
 * ChatCLI - Anthropic cache_control breakpoint planner
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import "sync/atomic"

// anthropicMaxCacheBreakpoints is Anthropic's hard cap on the number of
// content blocks that may carry cache_control in a single request.
const anthropicMaxCacheBreakpoints = 4

var cacheBlocksCoalesced atomic.Uint64

// CacheBlocksCoalescedTotal returns the cumulative count of cache_control
// markers that were coalesced (had their marker removed) to keep requests
// under Anthropic's per-request breakpoint cap.
func CacheBlocksCoalescedTotal() uint64 {
	return cacheBlocksCoalesced.Load()
}

// coalesceCacheControl removes cache_control markers from the earliest
// blocks until at most maxMarkers remain. The block content itself is
// untouched — only the marker is dropped.
//
// Why this is safe: Anthropic's prompt cache is prefix-based. A marker
// creates a cache breakpoint and everything BEFORE it (plus the marked
// block) becomes a cacheable layer. Dropping an earlier marker only
// removes that specific layer; the same content is still cached as part
// of any later marker's prefix. So we never lose cached tokens — we only
// lose the ability to invalidate at finer granularity, which is rarely
// useful in practice (the prefix typically changes together).
//
// We keep the LATEST markers because each later marker covers strictly
// more content than earlier ones, maximizing cache coverage with the
// markers we are allowed to use.
func coalesceCacheControl(blocks []map[string]interface{}, maxMarkers int) []map[string]interface{} {
	if maxMarkers < 0 {
		maxMarkers = 0
	}
	var positions []int
	for i, b := range blocks {
		if _, ok := b["cache_control"]; ok {
			positions = append(positions, i)
		}
	}
	if len(positions) <= maxMarkers {
		return blocks
	}
	dropCount := len(positions) - maxMarkers
	for _, pos := range positions[:dropCount] {
		delete(blocks[pos], "cache_control")
	}
	cacheBlocksCoalesced.Add(uint64(dropCount))
	return blocks
}
