package lessonq

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestRetryPolicy_ExponentialRamp(t *testing.T) {
	p := RetryPolicy{
		InitialDelay:   100 * time.Millisecond,
		MaxDelay:       10 * time.Second,
		Multiplier:     2.0,
		JitterFraction: 0, // deterministic for this check
		MaxAttempts:    10,
	}
	// attempt 1 = base, 2 = base*2, 3 = base*4, etc.
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1600 * time.Millisecond,
	}
	for i, w := range want {
		got := p.NextDelay(i+1, nil)
		if got != w {
			t.Fatalf("attempt %d: want %s; got %s", i+1, w, got)
		}
	}
}

func TestRetryPolicy_CapsAtMaxDelay(t *testing.T) {
	p := RetryPolicy{
		InitialDelay:   1 * time.Second,
		MaxDelay:       5 * time.Second,
		Multiplier:     10.0, // ramps fast
		JitterFraction: 0,
		MaxAttempts:    10,
	}
	// Attempt 3: 1s * 10 * 10 = 100s, must cap at 5s.
	if got := p.NextDelay(5, nil); got != 5*time.Second {
		t.Fatalf("expected cap at 5s; got %s", got)
	}
}

func TestRetryPolicy_JitterBounds(t *testing.T) {
	p := RetryPolicy{
		InitialDelay:   100 * time.Millisecond,
		MaxDelay:       time.Minute,
		Multiplier:     1.0, // no ramp
		JitterFraction: 0.25,
		MaxAttempts:    10,
	}
	rng := rand.New(rand.NewSource(42))
	min := time.Duration(math.MaxInt64)
	max := time.Duration(0)
	for i := 0; i < 1000; i++ {
		d := p.NextDelay(1, rng)
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	// With ±25% jitter around 100ms, bounds are [75ms, 125ms].
	if min < 70*time.Millisecond {
		t.Errorf("jitter below expected: min=%s", min)
	}
	if max > 130*time.Millisecond {
		t.Errorf("jitter above expected: max=%s", max)
	}
}

func TestRetryPolicy_ShouldRetry(t *testing.T) {
	// MaxAttempts=3 means a job may be attempted up to 3 times total.
	// Runner calls ShouldRetry(attempts) where attempts is the count
	// of tries already made (including the one that just failed).
	p := RetryPolicy{MaxAttempts: 3}
	want := []bool{true, true, true, false, false}
	for i, w := range want {
		if got := p.ShouldRetry(i); got != w {
			t.Errorf("attempts=%d: want %v; got %v", i, w, got)
		}
	}
}

func TestRetryPolicy_NormalizesZeroValues(t *testing.T) {
	var p RetryPolicy // all zero — must produce sane defaults
	if d := p.NextDelay(1, nil); d <= 0 {
		t.Fatalf("zero policy must produce positive delay; got %s", d)
	}
	if !p.ShouldRetry(0) {
		t.Fatal("zero policy must allow at least one retry")
	}
}
