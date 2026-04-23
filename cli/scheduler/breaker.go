/*
 * ChatCLI - Scheduler: circuit breaker.
 *
 * A breaker protects the scheduler from a cascading failure mode: the
 * k8s API is down → every k8s_resource_ready evaluator times out →
 * hundreds of jobs stack up polling, saturating the worker pool and
 * preventing unrelated jobs from making progress.
 *
 * Breaker states (classic three-state pattern):
 *
 *   Closed    — calls pass through. Failures are counted. When the
 *               count crosses the threshold within a sliding window,
 *               trip to Open.
 *
 *   Open      — calls short-circuit with ErrBreakerOpen. After
 *               cooldown elapses, allow one probe by transitioning
 *               to HalfOpen.
 *
 *   HalfOpen  — exactly one probe permitted; subsequent callers
 *               short-circuit until the probe result arrives.
 *                  probe success → Closed
 *                  probe failure → Open (cooldown restarted)
 *
 * Granularity: one breaker per (kind, key) — e.g. one for the
 * k8s_resource_ready evaluator type as a whole, another for each
 * shell action's command class. The breakerGroup holds them.
 */
package scheduler

import (
	"sync"
	"sync/atomic"
	"time"
)

// BreakerState is the exposed lifecycle state. Kept as a small int so
// atomic loads are cheap on the hot path.
type BreakerState int32

const (
	BreakerClosed BreakerState = iota
	BreakerOpen
	BreakerHalfOpen
)

// String makes states log-friendly.
func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half_open"
	}
	return "unknown"
}

// BreakerConfig tunes the trip thresholds. Zero values inherit scheduler
// defaults — see breakerGroup.NewBreaker.
type BreakerConfig struct {
	// FailureThreshold is how many failures within Window trip the breaker.
	FailureThreshold int
	// Window is the sliding window during which failures are counted.
	Window time.Duration
	// Cooldown is how long to stay Open before a HalfOpen probe.
	Cooldown time.Duration
	// HalfOpenSuccessRequired is how many consecutive successes close
	// the breaker from HalfOpen. Default 1 — one probe is enough.
	HalfOpenSuccessRequired int
}

func (c BreakerConfig) withDefaults() BreakerConfig {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.Window <= 0 {
		c.Window = 60 * time.Second
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 30 * time.Second
	}
	if c.HalfOpenSuccessRequired <= 0 {
		c.HalfOpenSuccessRequired = 1
	}
	return c
}

// Breaker is the per-(kind,key) circuit breaker. Safe for concurrent use.
type Breaker struct {
	key    string
	cfg    BreakerConfig
	state  atomic.Int32
	mu     sync.Mutex
	fails  []time.Time // timestamps within window
	nextOK time.Time   // earliest time a HalfOpen probe is allowed
	// halfOpenProbeInFlight is set true while a single probe is pending.
	// Subsequent Acquire attempts short-circuit until the probe completes.
	halfOpenProbeInFlight bool
	halfOpenWins          int
	onStateChange         func(key string, from, to BreakerState)
}

// Acquire asks the breaker whether a call may proceed. Returns:
//   - (true, nil)  → call allowed. Caller must invoke Release after.
//   - (false, err) → call denied. err wraps ErrBreakerOpen.
//
// The returned release callback records success/failure outcome.
// On (false, err) the release callback is a no-op.
func (b *Breaker) Acquire() (release func(success bool), err error) {
	state := BreakerState(b.state.Load())
	switch state {
	case BreakerClosed:
		return b.releaseClosed, nil
	case BreakerOpen:
		b.mu.Lock()
		defer b.mu.Unlock()
		if time.Now().Before(b.nextOK) {
			return noopRelease, ErrBreakerOpen
		}
		// Cooldown elapsed — attempt HalfOpen transition.
		if !b.halfOpenProbeInFlight {
			b.halfOpenProbeInFlight = true
			b.setStateLocked(BreakerHalfOpen)
			return b.releaseHalfOpen, nil
		}
		return noopRelease, ErrBreakerOpen
	case BreakerHalfOpen:
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.halfOpenProbeInFlight {
			return noopRelease, ErrBreakerOpen
		}
		b.halfOpenProbeInFlight = true
		return b.releaseHalfOpen, nil
	}
	return noopRelease, ErrBreakerOpen
}

// State reports the current breaker state for /jobs show and metrics.
func (b *Breaker) State() BreakerState { return BreakerState(b.state.Load()) }

// Key returns the breaker's identifier.
func (b *Breaker) Key() string { return b.key }

// ─── internals ─────────────────────────────────────────────────

func (b *Breaker) releaseClosed(success bool) {
	if success {
		return
	}
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails = append(b.fails, now)
	// Drop timestamps outside the window.
	cutoff := now.Add(-b.cfg.Window)
	for len(b.fails) > 0 && b.fails[0].Before(cutoff) {
		b.fails = b.fails[1:]
	}
	if len(b.fails) >= b.cfg.FailureThreshold {
		b.setStateLocked(BreakerOpen)
		b.nextOK = now.Add(b.cfg.Cooldown)
		b.fails = nil
	}
}

func (b *Breaker) releaseHalfOpen(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.halfOpenProbeInFlight = false
	if success {
		b.halfOpenWins++
		if b.halfOpenWins >= b.cfg.HalfOpenSuccessRequired {
			b.halfOpenWins = 0
			b.setStateLocked(BreakerClosed)
			b.fails = nil
		}
	} else {
		b.halfOpenWins = 0
		b.setStateLocked(BreakerOpen)
		b.nextOK = time.Now().Add(b.cfg.Cooldown)
	}
}

func (b *Breaker) setStateLocked(to BreakerState) {
	from := BreakerState(b.state.Load())
	if from == to {
		return
	}
	b.state.Store(int32(to))
	if b.onStateChange != nil {
		// Fire outside the lock to avoid reentrancy deadlocks.
		cb := b.onStateChange
		go cb(b.key, from, to)
	}
}

func noopRelease(_ bool) {}

// ─── Group ─────────────────────────────────────────────────────

// breakerGroup holds one Breaker per key. Used by the scheduler to
// track one breaker per condition type and one per action type.
type breakerGroup struct {
	mu            sync.RWMutex
	bs            map[string]*Breaker
	cfg           BreakerConfig
	onStateChange func(key string, from, to BreakerState)
}

func newBreakerGroup(cfg BreakerConfig, onChange func(string, BreakerState, BreakerState)) *breakerGroup {
	return &breakerGroup{
		bs:            make(map[string]*Breaker),
		cfg:           cfg.withDefaults(),
		onStateChange: onChange,
	}
}

// Get returns the breaker for key, constructing it on first access.
func (g *breakerGroup) Get(key string) *Breaker {
	g.mu.RLock()
	b, ok := g.bs[key]
	g.mu.RUnlock()
	if ok {
		return b
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if b, ok := g.bs[key]; ok {
		return b
	}
	b = &Breaker{
		key:           key,
		cfg:           g.cfg,
		onStateChange: g.onStateChange,
	}
	g.bs[key] = b
	return b
}

// Snapshot returns a copy of (key, state) for every known breaker.
// Used by /jobs show and the daemon diagnostic endpoint.
func (g *breakerGroup) Snapshot() map[string]BreakerState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make(map[string]BreakerState, len(g.bs))
	for k, v := range g.bs {
		out[k] = v.State()
	}
	return out
}
