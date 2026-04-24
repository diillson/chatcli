/*
 * ChatCLI - Scheduler: retry / backoff math.
 *
 * Isolated here so unit tests can exercise the ramp without spinning
 * up a full Scheduler. The shape is identical to the Reflexion queue's
 * retry policy (RetryPolicy.NextDelay in lessonq): expo with cap +
 * uniform jitter.
 */
package scheduler

import (
	"math"
	"math/rand"
	"time"
)

// DefaultBackoff returns the production default: 1s → 5min ramp,
// factor 2, 20% jitter, 5 retries.
func DefaultBackoff() Budget {
	return Budget{
		BackoffInitial: 1 * time.Second,
		BackoffMax:     5 * time.Minute,
		BackoffMult:    2.0,
		BackoffJitter:  0.2,
		MaxRetries:     5,
	}
}

// nextDelay computes the delay before the (attempt+1)-th retry, given
// that attempt Attempts have already happened (0-indexed: attempt=0
// means "the first retry after the first failure").
//
// rng is the Scheduler's shared *rand.Rand, mutex-protected upstream.
func nextDelay(b Budget, attempt int, rng *rand.Rand) time.Duration {
	if b.BackoffInitial <= 0 {
		b = b.Merge(DefaultBackoff())
	}
	mult := b.BackoffMult
	if mult <= 1 {
		mult = 2.0
	}
	// Unchecked pow could overflow; clamp.
	factor := math.Pow(mult, float64(attempt))
	if math.IsInf(factor, 0) || factor > 1e6 {
		factor = 1e6
	}
	delay := time.Duration(float64(b.BackoffInitial) * factor)
	if b.BackoffMax > 0 && delay > b.BackoffMax {
		delay = b.BackoffMax
	}
	// Jitter: uniform [-J, +J] * delay.
	if b.BackoffJitter > 0 && rng != nil {
		j := b.BackoffJitter
		if j > 0.5 {
			j = 0.5
		}
		offset := (rng.Float64()*2 - 1) * j * float64(delay)
		delay = time.Duration(float64(delay) + offset)
	}
	if delay < 0 {
		delay = 0
	}
	return delay
}

// shouldRetry reports whether another attempt is permitted given the
// budget and how many attempts have already happened.
func shouldRetry(b Budget, attempts int) bool {
	if b.MaxRetries <= 0 {
		return false
	}
	return attempts <= b.MaxRetries
}
