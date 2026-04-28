/*
 * ChatCLI - Anthropic cache_control breakpoint planner (Bedrock)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import "sync/atomic"

// anthropicMaxCacheBreakpoints is Anthropic's hard cap on the number of
// content blocks that may carry cache_control in a single request. The
// cap applies identically to direct Anthropic API calls and to
// Anthropic-family models hosted on Bedrock, which speak the same body
// schema.
const anthropicMaxCacheBreakpoints = 4

var cacheBlocksCoalesced atomic.Uint64

// CacheBlocksCoalescedTotal returns the cumulative count of cache_control
// markers that were dropped to keep Bedrock-hosted Anthropic requests
// under the per-request breakpoint cap.
func CacheBlocksCoalescedTotal() uint64 {
	return cacheBlocksCoalesced.Load()
}

// enforceCacheControlBudget walks the assembled request body and drops
// the earliest cache_control markers until at most maxMarkers remain
// across `system`, `tools`, and message content blocks combined.
//
// Mirrors the Anthropic adapter's defensive boundary check: per-section
// coalesce is enough on the happy path, but mode transitions and any
// future producer that stamps a marker outside the helper functions
// would silently push the request over the cap. Running this once on
// the fully-assembled reqBody right before json.Marshal makes the cap
// a structural invariant of the wire format.
//
// Earliest markers are dropped first because Anthropic's prompt cache
// is prefix-based: a later marker covers strictly more content, so
// keeping the latest maximizes cache coverage. Block content is never
// discarded — only the marker.
func enforceCacheControlBudget(reqBody map[string]interface{}, maxMarkers int) {
	if reqBody == nil {
		return
	}
	if maxMarkers < 0 {
		maxMarkers = 0
	}
	var holders []map[string]interface{}

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
