/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The prompt-cache telemetry as a persisted record.
 *
 * The counters in cost_cache_telemetry.go lived only in memory: /cost
 * showed them for the running session and they died with the process, so
 * two sessions could never be compared and a regression in prefix
 * stability had no history to show against. The snapshot below rides in
 * the session's cost file next to the token counts, is restored with it,
 * and carries the two figures a reader compares first — the hit share and
 * the write/read ratio — precomputed so a listing can print them without
 * knowing the schema of the provider that produced them.
 */
package cli

import "strings"

// CacheProviderSnapshot is one provider's slice of the telemetry.
type CacheProviderSnapshot struct {
	Requests    int   `json:"requests"`
	ReadTokens  int64 `json:"read_tokens"`
	WriteTokens int64 `json:"write_tokens,omitempty"`
	InputTokens int64 `json:"input_tokens"`
}

// CacheTelemetrySnapshot is the serializable form of the session's
// prompt-cache telemetry.
type CacheTelemetrySnapshot struct {
	Requests     int   `json:"requests"`
	Misses       int   `json:"misses,omitempty"`
	Expired      int   `json:"expired,omitempty"`
	Rebuilds     int   `json:"rebuilds,omitempty"`
	IdleExpiries int   `json:"idle_expiries,omitempty"`
	ReadTokens   int64 `json:"read_tokens"`
	WriteTokens  int64 `json:"write_tokens,omitempty"`
	InputTokens  int64 `json:"input_tokens"`
	// LastProvider and LastModel name the route the aggregate figures
	// describe; Additive says how that route counts its cache tokens.
	LastProvider string `json:"last_provider,omitempty"`
	LastModel    string `json:"last_model,omitempty"`
	Additive     bool   `json:"additive,omitempty"`
	// TTL is the cache lifetime in effect when the snapshot was written;
	// TTLPromoted records that the session promoted it on evidence, so a
	// restored session does not promote twice.
	TTL         string `json:"ttl,omitempty"`
	TTLPromoted bool   `json:"ttl_promoted,omitempty"`
	// HitPct and WriteReadRatio are derived at write time for readers.
	// The ratio is cache writes over cache reads: the share of the
	// session's cached traffic that was paid at the write price. A stable
	// prefix that only grows sits well under 1; a session that rewrote
	// its prefix climbs toward and past it.
	HitPct         float64 `json:"hit_pct"`
	WriteReadRatio float64 `json:"write_read_ratio"`

	ByProvider map[string]CacheProviderSnapshot `json:"by_provider,omitempty"`
}

// writeReadRatio is writes over reads, 0 when nothing was read.
func writeReadRatio(write, read int64) float64 {
	if read <= 0 {
		return 0
	}
	return float64(write) / float64(read)
}

// snapshot serializes the telemetry. Nil when no request reported cache
// fields, so a session without a cache carries no cache record.
func (c *cacheTelemetry) snapshot(ttlPromoted bool) *CacheTelemetrySnapshot {
	if c == nil || c.requests == 0 {
		return nil
	}
	s := &CacheTelemetrySnapshot{
		Requests:     c.requests,
		Misses:       c.misses,
		Expired:      c.expired,
		Rebuilds:     c.rebuilds,
		IdleExpiries: c.idleExpiries,
		ReadTokens:   c.readTokens,
		WriteTokens:  c.writeTokens,
		InputTokens:  c.inputTokens,
		LastProvider: c.lastProvider,
		LastModel:    c.lastModel,
		Additive:     c.lastAdditive,
		TTL:          cacheTTLIfResolvedFor(c.lastProvider, c.lastModel),
		TTLPromoted:  ttlPromoted,
	}
	if len(c.byProvider) > 0 {
		s.ByProvider = make(map[string]CacheProviderSnapshot, len(c.byProvider))
		for name, b := range c.byProvider {
			s.ByProvider[name] = CacheProviderSnapshot{
				Requests: b.requests, ReadTokens: b.readTokens, WriteTokens: b.writeTokens, InputTokens: b.inputTokens,
			}
		}
	}
	read, write, input := c.readTokens, c.writeTokens, c.inputTokens
	if b := c.byProvider[strings.ToUpper(strings.TrimSpace(c.lastProvider))]; b != nil {
		read, write, input = b.readTokens, b.writeTokens, b.inputTokens
	}
	s.HitPct = cacheHitPct(c.lastAdditive, read, write, input)
	s.WriteReadRatio = writeReadRatio(write, read)
	return s
}

// restore loads a persisted snapshot into the telemetry, replacing what
// the tracker held. A nil snapshot (an older cost file) resets it, so a
// restored session never inherits another session's counters. The
// per-request judgement state (what the previous request left readable)
// is not persisted: the first request after a restore is judged as a
// first request, which can only under-count misses, never invent one.
func (c *cacheTelemetry) restore(s *CacheTelemetrySnapshot) {
	if c == nil {
		return
	}
	*c = cacheTelemetry{}
	if s == nil {
		return
	}
	c.requests = s.Requests
	c.misses = s.Misses
	c.expired = s.Expired
	c.rebuilds = s.Rebuilds
	c.idleExpiries = s.IdleExpiries
	c.readTokens = s.ReadTokens
	c.writeTokens = s.WriteTokens
	c.inputTokens = s.InputTokens
	c.lastProvider = s.LastProvider
	c.lastModel = s.LastModel
	c.lastAdditive = s.Additive
	for name, b := range s.ByProvider {
		c.byProvider = c.bucketInit()
		c.byProvider[strings.ToUpper(strings.TrimSpace(name))] = &providerCache{
			requests: b.Requests, readTokens: b.ReadTokens, writeTokens: b.WriteTokens, inputTokens: b.InputTokens,
		}
	}
}

// bucketInit returns the provider map, creating it.
func (c *cacheTelemetry) bucketInit() map[string]*providerCache {
	if c.byProvider == nil {
		c.byProvider = make(map[string]*providerCache)
	}
	return c.byProvider
}
