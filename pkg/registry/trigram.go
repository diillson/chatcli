/*
 * ChatCLI - Trigram Fuzzy Search Cache
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package registry

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

const (
	defaultCacheSize    = 50
	defaultCacheTTL     = 5 * time.Minute
	similarityThreshold = 0.7
	minTrigramQueryLen  = 3
)

// TrigramCache provides fuzzy-matching local search over cached skill metadata.
// Uses trigram-based Jaccard similarity to match similar queries without network calls.
type TrigramCache struct {
	entries map[string]*cacheEntry
	lruList *list.List
	lruMap  map[string]*list.Element
	maxSize int
	ttl     time.Duration
	mu      sync.RWMutex
}

type cacheEntry struct {
	query    string
	results  []SkillMeta
	trigrams map[string]bool
	cachedAt time.Time
}

// NewTrigramCache creates a new trigram-based fuzzy search cache.
func NewTrigramCache(maxSize int, ttl time.Duration) *TrigramCache {
	if maxSize <= 0 {
		maxSize = defaultCacheSize
	}
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return &TrigramCache{
		entries: make(map[string]*cacheEntry),
		lruList: list.New(),
		lruMap:  make(map[string]*list.Element),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// Get retrieves cached results for an exact or fuzzy match.
// Returns nil if no match found or all matches are expired.
func (tc *TrigramCache) Get(query string) []SkillMeta {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	query = strings.ToLower(strings.TrimSpace(query))

	// 1. Try exact match first
	if entry, ok := tc.entries[query]; ok {
		if time.Since(entry.cachedAt) < tc.ttl {
			tc.touchLRU(query)
			return entry.results
		}
	}

	// 2. Try fuzzy match via trigram similarity
	if len(query) < minTrigramQueryLen {
		return nil
	}

	queryTrigrams := ExtractTrigrams(query)
	var bestMatch *cacheEntry
	var bestSimilarity float64

	for _, entry := range tc.entries {
		if time.Since(entry.cachedAt) >= tc.ttl {
			continue
		}
		sim := JaccardSimilarity(queryTrigrams, entry.trigrams)
		if sim >= similarityThreshold && sim > bestSimilarity {
			bestSimilarity = sim
			bestMatch = entry
		}
	}

	if bestMatch != nil {
		tc.touchLRU(bestMatch.query)
		return bestMatch.results
	}

	return nil
}

// Put stores results for a query in the cache.
func (tc *TrigramCache) Put(query string, results []SkillMeta) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	query = strings.ToLower(strings.TrimSpace(query))

	// Evict if at capacity
	for len(tc.entries) >= tc.maxSize {
		tc.evictOldest()
	}

	entry := &cacheEntry{
		query:    query,
		results:  results,
		trigrams: ExtractTrigrams(query),
		cachedAt: time.Now(),
	}

	// Update or insert
	if _, exists := tc.entries[query]; exists {
		tc.entries[query] = entry
		tc.touchLRU(query)
	} else {
		tc.entries[query] = entry
		elem := tc.lruList.PushFront(query)
		tc.lruMap[query] = elem
	}
}

// Invalidate removes a specific query from the cache.
func (tc *TrigramCache) Invalidate(query string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	query = strings.ToLower(strings.TrimSpace(query))
	tc.removeEntry(query)
}

// Clear removes all entries from the cache.
func (tc *TrigramCache) Clear() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tc.entries = make(map[string]*cacheEntry)
	tc.lruList.Init()
	tc.lruMap = make(map[string]*list.Element)
}

// Size returns the number of entries in the cache.
func (tc *TrigramCache) Size() int {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return len(tc.entries)
}

func (tc *TrigramCache) touchLRU(query string) {
	if elem, ok := tc.lruMap[query]; ok {
		tc.lruList.MoveToFront(elem)
	}
}

func (tc *TrigramCache) evictOldest() {
	back := tc.lruList.Back()
	if back == nil {
		return
	}
	query := back.Value.(string)
	tc.removeEntry(query)
}

func (tc *TrigramCache) removeEntry(query string) {
	delete(tc.entries, query)
	if elem, ok := tc.lruMap[query]; ok {
		tc.lruList.Remove(elem)
		delete(tc.lruMap, query)
	}
}

// ExtractTrigrams returns the set of 3-character substrings from a string.
func ExtractTrigrams(s string) map[string]bool {
	s = strings.ToLower(s)
	trigrams := make(map[string]bool)
	if len(s) < 3 {
		// For very short strings, use the string itself
		if len(s) > 0 {
			trigrams[s] = true
		}
		return trigrams
	}
	for i := 0; i <= len(s)-3; i++ {
		trigrams[s[i:i+3]] = true
	}
	return trigrams
}

// JaccardSimilarity computes |A intersection B| / |A union B|.
func JaccardSimilarity(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	intersection := 0
	for k := range a {
		if b[k] {
			intersection++
		}
	}

	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0.0
	}

	return float64(intersection) / float64(union)
}
