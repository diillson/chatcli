/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// queueSize bounds the hand-off between emitters and the pump. It
	// absorbs a burst (a wide parallel dispatch) without dropping.
	queueSize = 4096
	// DefaultSubscriberBuffer is the per-subscriber backlog used when a
	// caller passes a non-positive size.
	DefaultSubscriberBuffer = 1024
)

// Stats is a point-in-time reading of the bus counters.
type Stats struct {
	Enabled     bool   `json:"enabled"`
	Published   uint64 `json:"published"`
	Dropped     uint64 `json:"dropped"`      // queue full: event never reached the pump
	SlowDropped uint64 `json:"slow_dropped"` // a subscriber's buffer was full
	Subscribers int    `json:"subscribers"`
}

type subscriber struct {
	ch chan Event
}

// Bus fans events out to subscribers. The zero value is not usable; build
// one with New or use Default.
type Bus struct {
	instance string

	enabled     atomic.Bool
	seq         atomic.Uint64
	published   atomic.Uint64
	dropped     atomic.Uint64
	slowDropped atomic.Uint64

	queue    chan Event
	pumpOnce sync.Once

	mu           sync.Mutex
	subs         map[uint64]*subscriber
	nextSub      uint64
	snapshotters map[string]func() []Event
}

// New builds a disabled bus. An empty instance gets a random token, so two
// processes writing to the same spool never collide.
func New(instance string) *Bus {
	if instance == "" {
		instance = newInstanceToken()
	}
	return &Bus{
		instance:     instance,
		queue:        make(chan Event, queueSize),
		subs:         make(map[uint64]*subscriber),
		snapshotters: make(map[string]func() []Event),
	}
}

func newInstanceToken() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "local"
	}
	return hex.EncodeToString(b[:])
}

var (
	defaultOnce sync.Once
	defaultBus  *Bus
)

// Default returns the process-wide bus.
func Default() *Bus {
	defaultOnce.Do(func() { defaultBus = New("") })
	return defaultBus
}

// Emit publishes an event on the process-wide bus.
func Emit(ev Event) { Default().Emit(ev) }

// Enabled reports whether the process-wide bus is recording. Call sites
// that must do work to build an event guard on it.
func Enabled() bool { return Default().Enabled() }

// Instance returns the token identifying this process on the bus.
func (b *Bus) Instance() string {
	if b == nil {
		return ""
	}
	return b.instance
}

// Enabled reports whether the bus is recording.
func (b *Bus) Enabled() bool { return b != nil && b.enabled.Load() }

// SetEnabled turns recording on or off. On the rising edge every registered
// snapshotter is asked for the current state, so a dashboard opened in the
// middle of a run starts from what is live right now instead of from empty.
func (b *Bus) SetEnabled(on bool) {
	if b == nil {
		return
	}
	if !on {
		b.enabled.Store(false)
		return
	}
	b.pumpOnce.Do(func() { go b.pump() })
	if b.enabled.Swap(true) {
		return
	}
	for _, ev := range b.Snapshot() {
		b.Emit(ev.With("snapshot", "true"))
	}
}

// Emit publishes an event. It never blocks: with the bus off it returns
// after one atomic load, and with the queue full it drops and counts.
func (b *Bus) Emit(ev Event) {
	if b == nil || !b.enabled.Load() {
		return
	}
	ev.Seq = b.seq.Add(1)
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	ev.Instance = b.instance
	select {
	case b.queue <- ev:
		b.published.Add(1)
	default:
		b.dropped.Add(1)
	}
}

// pump is the single goroutine that moves events from the queue to the
// subscribers. Delivery is non-blocking per subscriber, so one stalled
// consumer can never hold up the others or back up into emitters.
func (b *Bus) pump() {
	for ev := range b.queue {
		b.mu.Lock()
		for _, s := range b.subs {
			select {
			case s.ch <- ev:
			default:
				b.slowDropped.Add(1)
			}
		}
		b.mu.Unlock()
	}
}

// Subscribe returns a channel of events and a cancel func. The channel is
// closed by cancel, which is safe to call more than once. A subscriber that
// falls more than buffer events behind loses events rather than slowing the
// bus; consumers detect the gap from Event.Seq.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = DefaultSubscriberBuffer
	}
	s := &subscriber{ch: make(chan Event, buffer)}
	b.mu.Lock()
	id := b.nextSub
	b.nextSub++
	b.subs[id] = s
	b.mu.Unlock()

	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, id)
			close(s.ch)
			b.mu.Unlock()
		})
	}
}

// RegisterSnapshotter registers (fn != nil) or removes (fn == nil) a source
// of current-state events under key. fn runs on the goroutine that enables
// the bus and must only read; the events it returns describe nodes that are
// live now (running agents, connected MCP servers, background processes).
func (b *Bus) RegisterSnapshotter(key string, fn func() []Event) {
	if b == nil || key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if fn == nil {
		delete(b.snapshotters, key)
		return
	}
	b.snapshotters[key] = fn
}

// Snapshot collects the current state from every snapshotter, in key order.
func (b *Bus) Snapshot() []Event {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	keys := make([]string, 0, len(b.snapshotters))
	for k := range b.snapshotters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fns := make([]func() []Event, 0, len(keys))
	for _, k := range keys {
		fns = append(fns, b.snapshotters[k])
	}
	b.mu.Unlock()

	var out []Event
	for _, fn := range fns {
		out = append(out, fn()...)
	}
	return out
}

// Stats reads the bus counters.
func (b *Bus) Stats() Stats {
	if b == nil {
		return Stats{}
	}
	b.mu.Lock()
	n := len(b.subs)
	b.mu.Unlock()
	return Stats{
		Enabled:     b.enabled.Load(),
		Published:   b.published.Load(),
		Dropped:     b.dropped.Load(),
		SlowDropped: b.slowDropped.Load(),
		Subscribers: n,
	}
}
