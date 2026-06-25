/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newClockedCoord(cooldown time.Duration, minItems int) (*runCoordinator, *fakeClock) {
	rc := newRunCoordinator(cooldown, minItems)
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	rc.now = clk.now
	return rc, clk
}

func TestCoordinatorBackPressure(t *testing.T) {
	rc, _ := newClockedCoord(2*time.Minute, 4)
	if !rc.tryAcquire(4) {
		t.Fatal("first acquire should succeed")
	}
	if rc.tryAcquire(4) {
		t.Fatal("a second concurrent run must be blocked")
	}
	rc.release()
	rc.recordSuccess()
	// Cooldown not elapsed → not due even though nothing is running.
	if rc.tryAcquire(4) {
		t.Fatal("cooldown should block the next run")
	}
}

func TestCoordinatorCadence(t *testing.T) {
	rc, clk := newClockedCoord(2*time.Minute, 4)
	if !rc.tryAcquire(4) {
		t.Fatal("initial acquire should succeed")
	}
	rc.release()
	rc.recordSuccess()

	// Too few new items, even after the cooldown.
	clk.advance(3 * time.Minute)
	if rc.tryAcquire(3) {
		t.Fatal("fewer than the minimum new items must not run")
	}
	// Enough items and cooldown elapsed → runs.
	if !rc.tryAcquire(4) {
		t.Fatal("should run after cooldown with enough new items")
	}
	rc.release()
}

func TestCoordinatorCircuitBreaker(t *testing.T) {
	rc, clk := newClockedCoord(2*time.Minute, 1)

	rc.recordFailure()
	rc.recordFailure()
	if rc.breakerOpen() {
		t.Fatal("breaker should still be closed below the threshold")
	}
	if n := rc.recordFailure(); n != 3 {
		t.Fatalf("failure streak = %d, want 3", n)
	}
	if !rc.breakerOpen() {
		t.Fatal("breaker should open at the threshold")
	}

	// Within the open window, runs are suppressed even past the cooldown.
	clk.advance(3 * time.Minute)
	if rc.tryAcquire(1) {
		t.Fatal("breaker must suppress runs while open")
	}

	// Past the window, runs resume.
	clk.advance(3 * time.Minute)
	if !rc.tryAcquire(1) {
		t.Fatal("breaker should have closed after its window")
	}
	rc.release()

	// A success clears the breaker and the streak.
	rc.recordFailure()
	rc.recordFailure()
	rc.recordFailure()
	rc.recordSuccess()
	if rc.breakerOpen() || rc.consecutiveFails != 0 {
		t.Fatal("success must reset the breaker and streak")
	}
}
