/*
 * ChatCLI - Convergence: per-scorer circuit breaker.
 *
 * Keeps the package self-contained — intentionally does NOT import
 * the pipeline's circuit breaker. Convergence scorers have different
 * failure modes (provider 429 vs network timeout vs malformed
 * response) and different trip thresholds than pipeline hooks.
 */
package convergence

import (
	"sync"
	"sync/atomic"
	"time"
)

// breakerState is the observable state.
type breakerState int32

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

// String for logs/metrics.
func (s breakerState) String() string {
	switch s {
	case breakerClosed:
		return "closed"
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// circuitBreaker is the package-local breaker. Smaller and simpler
// than the pipeline's — convergence cascades already have a fallback
// path so a tripped breaker just drops a scorer from the chain.
type circuitBreaker struct {
	state            atomic.Int32
	consecutiveFails atomic.Int64
	openedAt         atomic.Int64
	threshold        int
	coolDown         time.Duration
	mu               sync.Mutex
}

// newBreaker returns a breaker with production defaults if the
// provided values are zero/negative.
func newBreaker(threshold int, coolDown time.Duration) *circuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	if coolDown <= 0 {
		coolDown = 30 * time.Second
	}
	return &circuitBreaker{threshold: threshold, coolDown: coolDown}
}

// Allow returns true when calls should pass through.
func (b *circuitBreaker) Allow() bool {
	s := breakerState(b.state.Load())
	if s != breakerOpen {
		return true
	}
	if time.Since(time.Unix(0, b.openedAt.Load())) >= b.coolDown {
		// Promote Open → HalfOpen once the cool-down elapses.
		b.mu.Lock()
		if breakerState(b.state.Load()) == breakerOpen {
			b.state.Store(int32(breakerHalfOpen))
		}
		b.mu.Unlock()
		return true
	}
	return false
}

// RecordSuccess closes the breaker (from HalfOpen) and clears the
// failure counter (from Closed).
func (b *circuitBreaker) RecordSuccess() {
	b.consecutiveFails.Store(0)
	b.mu.Lock()
	if breakerState(b.state.Load()) == breakerHalfOpen {
		b.state.Store(int32(breakerClosed))
	}
	b.mu.Unlock()
}

// RecordFailure increments and potentially trips.
func (b *circuitBreaker) RecordFailure() {
	n := b.consecutiveFails.Add(1)
	if breakerState(b.state.Load()) == breakerHalfOpen {
		b.trip()
		return
	}
	if int(n) >= b.threshold {
		b.trip()
	}
}

// State returns the current observable state.
func (b *circuitBreaker) State() breakerState {
	return breakerState(b.state.Load())
}

func (b *circuitBreaker) trip() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state.Store(int32(breakerOpen))
	b.openedAt.Store(time.Now().UnixNano())
}
