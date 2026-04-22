/*
 * ChatCLI - Convergence: embedding LRU cache.
 *
 * Embeddings cost real money (Voyage/OpenAI billed per token). During
 * an iterative refine loop, we often call the embedder with the SAME
 * text multiple times (e.g. comparing draft N against each of drafts
 * N-1, N-2 for stability checks). A tiny LRU with TTL eliminates
 * those duplicate calls without hoarding vectors forever.
 *
 * The cache is keyed by SHA-256(text) so we never fingerprint raw
 * text into memory longer than needed, and it's bounded by both size
 * (default 256 entries) and age (default 5 minutes).
 */
package convergence

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// cacheEntry holds the cached vector + timestamp for TTL eviction.
type cacheEntry struct {
	key      string
	vector   []float32
	cachedAt time.Time
	lruRef   *list.Element
}

// embedCache is a thread-safe bounded LRU with TTL.
type embedCache struct {
	mu      sync.Mutex
	entries map[string]*cacheEntry
	order   *list.List // front = most recent
	maxSize int
	ttl     time.Duration
}

// newEmbedCache builds a cache. maxSize ≤ 0 disables size bound
// (kept only for tests); ttl ≤ 0 disables TTL expiry.
func newEmbedCache(maxSize int, ttl time.Duration) *embedCache {
	if maxSize < 0 {
		maxSize = 0
	}
	return &embedCache{
		entries: make(map[string]*cacheEntry, maxSize),
		order:   list.New(),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// Get returns the cached vector for text, or (nil, false) on miss
// or TTL-expired entry.
func (c *embedCache) Get(text string) ([]float32, bool) {
	key := hashText(text)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.ttl > 0 && time.Since(e.cachedAt) > c.ttl {
		c.evictLocked(e)
		return nil, false
	}
	// Touch — move to front of LRU.
	c.order.MoveToFront(e.lruRef)
	return e.vector, true
}

// Put stores vector for text. Bounded eviction applies.
func (c *embedCache) Put(text string, vector []float32) {
	if len(vector) == 0 {
		return
	}
	key := hashText(text)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[key]; ok {
		existing.vector = vector
		existing.cachedAt = time.Now()
		c.order.MoveToFront(existing.lruRef)
		return
	}
	e := &cacheEntry{
		key:      key,
		vector:   vector,
		cachedAt: time.Now(),
	}
	e.lruRef = c.order.PushFront(e)
	c.entries[key] = e
	c.enforceSizeLocked()
}

// Len returns the current number of entries.
func (c *embedCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Clear empties the cache — useful for tests and config reload.
func (c *embedCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cacheEntry)
	c.order.Init()
}

func (c *embedCache) enforceSizeLocked() {
	if c.maxSize <= 0 {
		return
	}
	for len(c.entries) > c.maxSize {
		back := c.order.Back()
		if back == nil {
			return
		}
		e := back.Value.(*cacheEntry)
		c.evictLocked(e)
	}
}

func (c *embedCache) evictLocked(e *cacheEntry) {
	delete(c.entries, e.key)
	c.order.Remove(e.lruRef)
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}
