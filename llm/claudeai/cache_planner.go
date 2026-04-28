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

// enforceCacheControlBudget walks the assembled request body and drops
// the earliest cache_control markers until at most maxMarkers remain
// across `system`, `tools`, and message content blocks combined.
//
// This is the FINAL defensive layer. The per-section coalesce calls in
// buildMessagesAndSystem / buildOAuthMessagesAndSystem / SendPromptWithTools
// already keep each section under budget on the happy path, but mode
// transitions (chat ↔ coder ↔ agent) compose multiple system messages
// from different code paths, and any future producer that stamps a marker
// outside those helpers would silently push the request over Anthropic's
// hard cap of 4. Running this once on the fully-assembled reqBody right
// before json.Marshal makes the cap a structural invariant of the wire
// format, independent of how the body was built.
//
// Earliest markers are dropped first, matching the per-section policy:
// prefix-based caching means a later marker covers strictly more content,
// so keeping the latest maximizes cache coverage with the markers we are
// allowed to use. Block content is never discarded.
func enforceCacheControlBudget(reqBody map[string]interface{}, maxMarkers int) {
	if reqBody == nil {
		return
	}
	if maxMarkers < 0 {
		maxMarkers = 0
	}
	var holders []map[string]interface{}

	// Order matters: system is the outermost prefix, then tools, then
	// messages. We collect in that order so the "earliest" markers we
	// drop are correctly the system-block ones — those have the smallest
	// covered prefix and are the cheapest to lose.
	if v, ok := reqBody["system"]; ok {
		collectMarkerHolders(v, &holders)
	}
	if v, ok := reqBody["tools"]; ok {
		collectMarkerHolders(v, &holders)
	}
	if v, ok := reqBody["messages"]; ok {
		collectMarkerHoldersInMessages(v, &holders)
	}

	if len(holders) <= maxMarkers {
		return
	}
	drop := len(holders) - maxMarkers
	for i := 0; i < drop; i++ {
		delete(holders[i], "cache_control")
	}
	cacheBlocksCoalesced.Add(uint64(drop))
}

// collectMarkerHolders appends pointers to every map carrying a
// cache_control key found at the top level of a list-shaped value.
// Accepts both []map[string]interface{} (typed) and []interface{} (boxed,
// the OAuth path uses this when mixing helper-returned blocks with
// hand-assembled oauthTextBlock entries).
func collectMarkerHolders(v interface{}, out *[]map[string]interface{}) {
	switch s := v.(type) {
	case []map[string]interface{}:
		for _, m := range s {
			if m == nil {
				continue
			}
			if _, ok := m["cache_control"]; ok {
				*out = append(*out, m)
			}
		}
	case []interface{}:
		for _, item := range s {
			m, ok := item.(map[string]interface{})
			if !ok || m == nil {
				continue
			}
			if _, ok := m["cache_control"]; ok {
				*out = append(*out, m)
			}
		}
	}
}

// collectMarkerHoldersInMessages walks each message's "content" field
// and collects any cache_control marker carried on a content block. The
// non-tool path uses string content (never marked); the tool path uses
// list-shaped content with tool_use / tool_result entries (no markers
// today). This is defensive against a future change that stamps a
// marker on a content block — e.g. for cross-turn cached tool results.
func collectMarkerHoldersInMessages(v interface{}, out *[]map[string]interface{}) {
	walk := func(msg map[string]interface{}) {
		if msg == nil {
			return
		}
		collectMarkerHolders(msg["content"], out)
	}
	switch msgs := v.(type) {
	case []map[string]interface{}:
		for _, m := range msgs {
			walk(m)
		}
	case []interface{}:
		for _, item := range msgs {
			if m, ok := item.(map[string]interface{}); ok {
				walk(m)
			}
		}
	}
}
