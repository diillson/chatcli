/*
 * ChatCLI - Pipeline per-hook circuit breaker.
 *
 * A misbehaving hook (panicking, timing out, consistently erroring)
 * would otherwise be invoked on every turn. The circuit breaker
 * tracks failures within a sliding window and opens ("trips") when a
 * threshold is exceeded, skipping the hook for a cool-down period.
 *
 * State diagram:
 *
 *   Closed ──failures ≥ threshold──▶ Open ──cool-down elapsed──▶ HalfOpen
 *     ▲                                                              │
 *     │                                                              ▼
 *     └────────────────────success────────────────────────────── HalfOpen
 *     └──────failure (even one in half-open)──────▶ Open (fresh cool-down)
 *
 * The implementation is intentionally tiny (~100 lines) because the
 * hook count per pipeline is small (typically 1-3) and simplicity
 * beats the external gobreaker dep for this narrow use case.
 */
package quality

import (
	"sync"
	"sync/atomic"
	"time"
)

// BreakerState is the observable circuit state.
type BreakerState int32

const (
	BreakerClosed   BreakerState = iota // normal — calls pass through
	BreakerOpen                         // trip — calls short-circuited
	BreakerHalfOpen                     // cool-down elapsed; probing
)

// String makes states log/metric-friendly.
func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// BreakerConfig controls trip + recovery thresholds. Defaults are
// conservative (5 failures → 30s cool-down) so the breaker only
// fires on genuinely broken hooks.
type BreakerConfig struct {
	FailureThreshold int           // consecutive failures to trip
	CoolDown         time.Duration // time in Open before transitioning to HalfOpen
}

// DefaultBreakerConfig returns production defaults.
func DefaultBreakerConfig() BreakerConfig {
	return BreakerConfig{
		FailureThreshold: 5,
		CoolDown:         30 * time.Second,
	}
}

// breaker is a per-hook circuit breaker instance.
type breaker struct {
	cfg              BreakerConfig
	state            atomic.Int32
	consecutiveFails atomic.Int64
	openedAt         atomic.Int64 // unix nanos when last tripped
	mu               sync.Mutex   // serializes state transitions only
}

func newBreaker(cfg BreakerConfig) *breaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.CoolDown <= 0 {
		cfg.CoolDown = 30 * time.Second
	}
	return &breaker{cfg: cfg}
}

// State returns the current state, transitioning Open→HalfOpen when
// the cool-down has elapsed. This lazy transition avoids needing a
// background goroutine per breaker.
func (b *breaker) State() BreakerState {
	s := BreakerState(b.state.Load())
	if s != BreakerOpen {
		return s
	}
	openedNanos := b.openedAt.Load()
	if time.Since(time.Unix(0, openedNanos)) < b.cfg.CoolDown {
		return BreakerOpen
	}
	// Promote Open → HalfOpen. CAS so concurrent callers both get
	// consistent state.
	b.mu.Lock()
	defer b.mu.Unlock()
	if BreakerState(b.state.Load()) == BreakerOpen {
		b.state.Store(int32(BreakerHalfOpen))
	}
	return BreakerState(b.state.Load())
}

// Allow returns true when the breaker permits a call. Equivalent to
// State() != Open, with the Open→HalfOpen promotion.
func (b *breaker) Allow() bool { return b.State() != BreakerOpen }

// RecordSuccess resets consecutive-failure count and closes the
// breaker if it was HalfOpen.
func (b *breaker) RecordSuccess() {
	b.consecutiveFails.Store(0)
	if BreakerState(b.state.Load()) == BreakerHalfOpen {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.state.Store(int32(BreakerClosed))
	}
}

// RecordFailure increments the failure count. When the threshold is
// hit (or we're in HalfOpen), trips the breaker. A single failure in
// HalfOpen re-opens with a fresh cool-down.
func (b *breaker) RecordFailure() {
	n := b.consecutiveFails.Add(1)
	cur := BreakerState(b.state.Load())
	if cur == BreakerHalfOpen {
		b.trip()
		return
	}
	if int(n) >= b.cfg.FailureThreshold {
		b.trip()
	}
}

func (b *breaker) trip() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state.Store(int32(BreakerOpen))
	b.openedAt.Store(time.Now().UnixNano())
}
