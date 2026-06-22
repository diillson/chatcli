/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Metrics accumulates compression accounting for a session. It mirrors the
// counter-with-snapshot pattern used elsewhere (e.g. cache_planner's
// cacheBlocksCoalesced) and is safe for concurrent use. A nil *Metrics is a
// valid no-op receiver, so callers never need a nil check.
type Metrics struct {
	calls      atomic.Int64
	reductions atomic.Int64 // calls that actually shrank the payload
	bytesIn    atomic.Int64
	bytesOut   atomic.Int64
	ccrPuts    atomic.Int64
	ccrHits    atomic.Int64
	ccrMisses  atomic.Int64
	ccrDedupes atomic.Int64 // Put of content already stored

	mu         sync.Mutex
	byStrategy map[string]*strategyCounter
}

type strategyCounter struct {
	calls    int64
	bytesIn  int64
	bytesOut int64
}

// NewMetrics returns a ready accumulator.
func NewMetrics() *Metrics {
	return &Metrics{byStrategy: make(map[string]*strategyCounter)}
}

// RecordCompression accounts for one Compress call. Safe on a nil receiver.
func (m *Metrics) RecordCompression(r Result) {
	if m == nil {
		return
	}
	m.calls.Add(1)
	m.bytesIn.Add(int64(r.OriginalSize))
	m.bytesOut.Add(int64(r.CompressedSize))
	if r.CompressedSize < r.OriginalSize {
		m.reductions.Add(1)
	}

	m.mu.Lock()
	if m.byStrategy == nil {
		m.byStrategy = make(map[string]*strategyCounter)
	}
	c := m.byStrategy[r.Strategy]
	if c == nil {
		c = &strategyCounter{}
		m.byStrategy[r.Strategy] = c
	}
	c.calls++
	c.bytesIn += int64(r.OriginalSize)
	c.bytesOut += int64(r.CompressedSize)
	m.mu.Unlock()
}

// RecordCCRPut / RecordCCRDedupe / RecordCCRHit / RecordCCRMiss track the
// reversible-store side of the layer. All safe on a nil receiver.
func (m *Metrics) RecordCCRPut() {
	if m != nil {
		m.ccrPuts.Add(1)
	}
}
func (m *Metrics) RecordCCRDedupe() {
	if m != nil {
		m.ccrDedupes.Add(1)
	}
}
func (m *Metrics) RecordCCRHit() {
	if m != nil {
		m.ccrHits.Add(1)
	}
}
func (m *Metrics) RecordCCRMiss() {
	if m != nil {
		m.ccrMisses.Add(1)
	}
}

// StrategyStat is a per-strategy line in a Snapshot.
type StrategyStat struct {
	Strategy string
	Calls    int64
	BytesIn  int64
	BytesOut int64
}

// Stats is an immutable snapshot of the accumulated metrics, suitable for
// /compression stats and the cost footer.
type Stats struct {
	Calls      int64
	Reductions int64
	BytesIn    int64
	BytesOut   int64
	CCRPuts    int64
	CCRDedupes int64
	CCRHits    int64
	CCRMisses  int64
	ByStrategy []StrategyStat
}

// SavedBytes is the total prompt reduction (never negative).
func (s Stats) SavedBytes() int64 {
	if s.BytesOut >= s.BytesIn {
		return 0
	}
	return s.BytesIn - s.BytesOut
}

// Ratio is the aggregate BytesOut/BytesIn in [0,1]; 1.0 means no reduction.
func (s Stats) Ratio() float64 {
	if s.BytesIn == 0 {
		return 1.0
	}
	return float64(s.BytesOut) / float64(s.BytesIn)
}

// Snapshot returns a consistent copy of the current counters.
func (m *Metrics) Snapshot() Stats {
	if m == nil {
		return Stats{}
	}
	s := Stats{
		Calls:      m.calls.Load(),
		Reductions: m.reductions.Load(),
		BytesIn:    m.bytesIn.Load(),
		BytesOut:   m.bytesOut.Load(),
		CCRPuts:    m.ccrPuts.Load(),
		CCRDedupes: m.ccrDedupes.Load(),
		CCRHits:    m.ccrHits.Load(),
		CCRMisses:  m.ccrMisses.Load(),
	}
	m.mu.Lock()
	for strategy, c := range m.byStrategy {
		s.ByStrategy = append(s.ByStrategy, StrategyStat{
			Strategy: strategy, Calls: c.calls, BytesIn: c.bytesIn, BytesOut: c.bytesOut,
		})
	}
	m.mu.Unlock()
	sort.Slice(s.ByStrategy, func(i, j int) bool {
		return s.ByStrategy[i].BytesIn > s.ByStrategy[j].BytesIn
	})
	return s
}
