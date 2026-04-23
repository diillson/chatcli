/*
 * ChatCLI - Lesson Queue: retry policy.
 *
 * Computes back-off with full jitter (per AWS Architecture blog, 2015):
 * uniform random in [delay * (1-frac), delay * (1+frac)] instead of a
 * flat exponential. This prevents thundering-herd when many lessons
 * fail simultaneously (e.g. provider 429 storm).
 */
package lessonq

import (
	"math/rand"
	"time"
)

// NextDelay returns the backoff for the Nth attempt (1-indexed).
// Attempt 1 maps to InitialDelay (with jitter); each subsequent
// attempt multiplies by Multiplier, capped at MaxDelay.
//
// The rng parameter is injectable so tests can make jitter
// deterministic. Pass nil to use the global rand.
func (p RetryPolicy) NextDelay(attempt int, rng *rand.Rand) time.Duration {
	// Normalize defaults — a zero-valued RetryPolicy yields a sane
	// exponential ramp instead of zero-delay spin.
	p = p.normalize()
	if attempt < 1 {
		attempt = 1
	}

	// Geometric ramp with cap.
	delay := float64(p.InitialDelay)
	for i := 1; i < attempt; i++ {
		delay *= p.Multiplier
		if delay >= float64(p.MaxDelay) {
			delay = float64(p.MaxDelay)
			break
		}
	}

	if p.JitterFraction > 0 {
		jitter := 2*randFloat(rng)*p.JitterFraction - p.JitterFraction // [-frac, +frac]
		delay = delay * (1 + jitter)
	}

	if delay < 0 {
		delay = 0
	}
	return time.Duration(delay)
}

// ShouldRetry reports whether another attempt is allowed given the
// total attempts already made (the attempt that just failed counts).
func (p RetryPolicy) ShouldRetry(attempts int) bool {
	p = p.normalize()
	return attempts < p.MaxAttempts
}

func (p RetryPolicy) normalize() RetryPolicy {
	if p.InitialDelay <= 0 {
		p.InitialDelay = time.Second
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 5 * time.Minute
	}
	if p.MaxDelay < p.InitialDelay {
		p.MaxDelay = p.InitialDelay
	}
	if p.Multiplier < 1.0 {
		p.Multiplier = 2.0
	}
	if p.JitterFraction < 0 {
		p.JitterFraction = 0
	}
	if p.JitterFraction > 0.5 {
		p.JitterFraction = 0.5
	}
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 5
	}
	return p
}

// randFloat returns a pseudorandom float64 in [0.0, 1.0) for backoff
// jitter. We intentionally use math/rand (not crypto/rand) because:
//
//  1. Backoff jitter is a performance optimization to spread retries,
//     not a security primitive. An attacker predicting our jitter
//     gains nothing — they already know the backoff range from docs.
//  2. crypto/rand would block on entropy exhaustion in constrained
//     environments (containers, early boot), which would stall retry
//     scheduling and compound the original failure.
func randFloat(rng *rand.Rand) float64 {
	if rng == nil {
		return rand.Float64() // #nosec G404 -- jitter is not security-sensitive; see comment above
	}
	return rng.Float64()
}
