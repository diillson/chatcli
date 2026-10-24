/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * run_coordinator.go — the cadence/back-pressure/circuit-breaker primitive that
 * governs the background self-evolution pass (memory extraction + skill
 * detection share one run). Extracted from memoryWorker so the scheduling logic
 * is independently testable with an injectable clock, and reusable by any future
 * periodic task.
 *
 * It guarantees: at most one run at a time (back-pressure); a minimum gap and a
 * minimum amount of new work between runs (cadence); and, after a streak of
 * failures, a cooldown window during which runs are skipped entirely (circuit
 * breaker) instead of hammering a provider that is down — the queue is drained
 * on the next run once the window passes.
 */
package cli

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	// runBreakerThreshold is how many consecutive failures open the breaker.
	runBreakerThreshold = 3
	// runBreakerWindow is how long runs are skipped once the breaker is open.
	runBreakerWindow = 5 * time.Minute
)

// runCoordinator decides when the background pass may run and records outcomes.
// The atomic running flag is the back-pressure gate; the mutex guards cadence
// and breaker state. now is injectable for deterministic tests.
type runCoordinator struct {
	running atomic.Bool

	now         func() time.Time
	cooldown    time.Duration
	minNewItems int

	breakerThreshold int
	breakerWindow    time.Duration

	mu               sync.Mutex
	lastRun          time.Time
	consecutiveFails int
	openUntil        time.Time
}

func newRunCoordinator(cooldown time.Duration, minNewItems int) *runCoordinator {
	return &runCoordinator{
		now:              time.Now,
		cooldown:         cooldown,
		minNewItems:      minNewItems,
		breakerThreshold: runBreakerThreshold,
		breakerWindow:    runBreakerWindow,
	}
}

// isRunning reports whether a run is in progress (used to skip cheap nudges).
func (rc *runCoordinator) isRunning() bool { return rc.running.Load() }

// tryAcquire marks a run in progress and returns true only when one is due:
// nothing else running, enough new work, the cooldown elapsed and the breaker
// closed. A caller must only release() when tryAcquire returned true.
//
// Whether a run is due is decided BEFORE taking the gate: an attempt that is
// not due must never hold it, not even for an instant, or a due attempt
// racing it finds the gate taken and gives up, and with the not-due one
// releasing right after, no run happens at all (a queued RPC segment then
// waited for the next 3-minute tick). It is checked again under the gate in
// case a run finished in between.
func (rc *runCoordinator) tryAcquire(newItems int) bool {
	if !rc.due(newItems) {
		return false
	}
	if !rc.running.CompareAndSwap(false, true) {
		return false
	}
	if !rc.due(newItems) {
		rc.running.Store(false)
		return false
	}
	return true
}

// due reports whether a run with newItems of new work may start now.
func (rc *runCoordinator) due(newItems int) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	now := rc.now()
	return newItems >= rc.minNewItems &&
		now.Sub(rc.lastRun) >= rc.cooldown &&
		!now.Before(rc.openUntil)
}

// release clears the back-pressure gate.
func (rc *runCoordinator) release() { rc.running.Store(false) }

// recordSuccess advances the cadence clock and resets the failure streak and
// breaker.
func (rc *runCoordinator) recordSuccess() {
	rc.mu.Lock()
	rc.lastRun = rc.now()
	rc.consecutiveFails = 0
	rc.openUntil = time.Time{}
	rc.mu.Unlock()
}

// recordFailure advances the cadence clock, increments the failure streak and
// opens the breaker once the threshold is crossed. Returns the new streak length
// so the caller can drive its own user-facing notice.
func (rc *runCoordinator) recordFailure() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.lastRun = rc.now()
	rc.consecutiveFails++
	if rc.consecutiveFails >= rc.breakerThreshold {
		rc.openUntil = rc.now().Add(rc.breakerWindow)
	}
	return rc.consecutiveFails
}
