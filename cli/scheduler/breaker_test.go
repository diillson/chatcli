package scheduler

import (
	"testing"
	"time"
)

func TestBreaker_OpensAfterThreshold(t *testing.T) {
	g := newBreakerGroup(BreakerConfig{
		FailureThreshold: 3,
		Window:           time.Second,
		Cooldown:         100 * time.Millisecond,
	}, nil)
	b := g.Get("eval:http")
	for i := 0; i < 3; i++ {
		rel, err := b.Acquire()
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		rel(false)
	}
	if b.State() != BreakerOpen {
		t.Errorf("expected open after 3 failures, got %s", b.State())
	}
	rel, err := b.Acquire()
	if err == nil {
		rel(false)
		t.Error("expected breaker-open error")
	}
}

func TestBreaker_HalfOpenRecovery(t *testing.T) {
	g := newBreakerGroup(BreakerConfig{
		FailureThreshold:        2,
		Window:                  time.Second,
		Cooldown:                50 * time.Millisecond,
		HalfOpenSuccessRequired: 1,
	}, nil)
	b := g.Get("eval:tcp")
	for i := 0; i < 2; i++ {
		r, _ := b.Acquire()
		r(false)
	}
	if b.State() != BreakerOpen {
		t.Fatal("expected open")
	}
	// Wait past cooldown.
	time.Sleep(70 * time.Millisecond)
	rel, err := b.Acquire()
	if err != nil {
		t.Fatalf("half-open probe acquire: %v", err)
	}
	if b.State() != BreakerHalfOpen {
		t.Fatalf("expected half_open, got %s", b.State())
	}
	rel(true) // probe succeeded
	if b.State() != BreakerClosed {
		t.Errorf("expected closed after successful probe, got %s", b.State())
	}
}
