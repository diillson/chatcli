/*
 * Package ratelimit parses provider rate-limit response headers
 * (the x-ratelimit-* family used by OpenAI/OpenRouter/Anthropic-compatible
 * APIs) into structured buckets and keeps the latest snapshot per provider.
 *
 * It is observability, not enforcement: the agent and the /ratelimit command
 * read the snapshot to show how close a provider is to its quota and when it
 * resets. Recording is a cheap, read-only header scan done after each
 * response, so wiring it into a provider's response handler cannot change
 * request behavior.
 */
package ratelimit

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bucket is a single rate-limit dimension (requests or tokens).
type Bucket struct {
	Limit      int
	Remaining  int
	ResetSecs  float64 // seconds until reset, as reported by the provider
	CapturedAt time.Time
}

// Valid reports whether the bucket carries usable data.
func (b Bucket) Valid() bool { return b.Limit > 0 || b.Remaining > 0 }

// UsagePct returns the fraction of the limit consumed (0..1). Zero when the
// limit is unknown.
func (b Bucket) UsagePct() float64 {
	if b.Limit <= 0 {
		return 0
	}
	used := b.Limit - b.Remaining
	if used < 0 {
		used = 0
	}
	return float64(used) / float64(b.Limit)
}

// RemainingSeconds returns the seconds until reset, adjusted for time elapsed
// since the snapshot was captured (never negative).
func (b Bucket) RemainingSeconds() float64 {
	if b.CapturedAt.IsZero() {
		return b.ResetSecs
	}
	r := b.ResetSecs - time.Since(b.CapturedAt).Seconds()
	if r < 0 {
		return 0
	}
	return r
}

// Snapshot is the latest rate-limit state for one provider.
type Snapshot struct {
	Provider  string
	Requests  Bucket
	Tokens    Bucket
	UpdatedAt time.Time
}

// Has reports whether the snapshot carries any usable data.
func (s Snapshot) Has() bool { return s.Requests.Valid() || s.Tokens.Valid() }

// Parse extracts request/token buckets from response headers. It recognizes
// the OpenAI/OpenRouter form (x-ratelimit-*-requests/tokens with duration
// resets like "1m30s" or seconds) and is tolerant of missing fields.
func Parse(h http.Header) (req, tok Bucket) {
	now := time.Now()

	req.Limit = headerInt(h, "x-ratelimit-limit-requests")
	req.Remaining = headerInt(h, "x-ratelimit-remaining-requests")
	req.ResetSecs = headerDuration(h, "x-ratelimit-reset-requests")
	if req.Valid() {
		req.CapturedAt = now
	}

	tok.Limit = headerInt(h, "x-ratelimit-limit-tokens")
	tok.Remaining = headerInt(h, "x-ratelimit-remaining-tokens")
	tok.ResetSecs = headerDuration(h, "x-ratelimit-reset-tokens")
	if tok.Valid() {
		tok.CapturedAt = now
	}
	return req, tok
}

func headerInt(h http.Header, key string) int {
	v := strings.TrimSpace(h.Get(key))
	if v == "" {
		return 0
	}
	// Some providers suffix counts with units; keep leading digits.
	n, err := strconv.Atoi(v)
	if err == nil {
		return n
	}
	if f, ferr := strconv.ParseFloat(v, 64); ferr == nil {
		return int(f)
	}
	return 0
}

// headerDuration parses a reset value that may be a bare number of seconds
// ("60", "0.5") or a Go-style duration string ("1m30s", "6s", "300ms").
func headerDuration(h http.Header, key string) float64 {
	v := strings.TrimSpace(h.Get(key))
	if v == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d.Seconds()
	}
	return 0
}

// --- process-global registry ---

var (
	mu    sync.RWMutex
	store = map[string]Snapshot{}
)

// Record parses headers for a provider and stores the snapshot if it carries
// data. Safe for concurrent use. A no-op when headers carry nothing useful,
// so it never clobbers a good snapshot with an empty one.
func Record(provider string, h http.Header) {
	if provider == "" || h == nil {
		return
	}
	req, tok := Parse(h)
	if !req.Valid() && !tok.Valid() {
		return
	}
	mu.Lock()
	store[provider] = Snapshot{Provider: provider, Requests: req, Tokens: tok, UpdatedAt: time.Now()}
	mu.Unlock()
}

// Get returns the latest snapshot for a provider and whether one exists.
func Get(provider string) (Snapshot, bool) {
	mu.RLock()
	defer mu.RUnlock()
	s, ok := store[provider]
	return s, ok
}

// All returns a copy of every stored snapshot.
func All() []Snapshot {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Snapshot, 0, len(store))
	for _, s := range store {
		out = append(out, s)
	}
	return out
}

// Reset clears all snapshots (used by tests).
func Reset() {
	mu.Lock()
	store = map[string]Snapshot{}
	mu.Unlock()
}
