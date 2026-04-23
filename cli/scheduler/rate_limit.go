/*
 * ChatCLI - Scheduler: rate limiting.
 *
 * Two levels:
 *
 *   Global — protects the scheduler itself from enqueue storms. A
 *   token-bucket on total Enqueue rate; when exhausted, Enqueue
 *   returns ErrRateLimited with a Retry-After hint.
 *
 *   Per-owner — caps how many jobs a single agent or worker can create
 *   per minute. Prevents a runaway ReAct loop from filling the queue.
 *
 * Implemented on top of golang.org/x/time/rate, which is already a
 * dependency (used by the LLM manager's per-provider rate limits).
 */
package scheduler

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// rateLimiter composes one global limiter and one per-owner map.
type rateLimiter struct {
	global *rate.Limiter

	mu        sync.RWMutex
	perOwner  map[string]*rate.Limiter
	perOwnerR rate.Limit
	perOwnerB int
}

// newRateLimiter constructs a limiter. globalRPS ≤ 0 disables the
// global limit; perOwnerRPS ≤ 0 disables per-owner limits.
func newRateLimiter(globalRPS, perOwnerRPS float64, globalBurst, perOwnerBurst int) *rateLimiter {
	rl := &rateLimiter{
		perOwner:  make(map[string]*rate.Limiter),
		perOwnerR: rate.Limit(perOwnerRPS),
		perOwnerB: perOwnerBurst,
	}
	if globalRPS > 0 {
		burst := globalBurst
		if burst <= 0 {
			burst = int(globalRPS)
			if burst < 1 {
				burst = 1
			}
		}
		rl.global = rate.NewLimiter(rate.Limit(globalRPS), burst)
	}
	return rl
}

// Allow reports whether the caller may proceed. When not allowed,
// retryAfter hints at the next permitted window.
//
// A "delay" budget of 5ms is tolerated — Reserve/DelayFrom computes in
// float64 nanos and rounding errors produce sub-microsecond "delays"
// on the very first token of a fresh limiter. Rejecting those would
// cause legitimate first calls to fail with a phantom rate limit.
func (r *rateLimiter) Allow(owner Owner) (allowed bool, retryAfter time.Duration) {
	const tolerableDelay = 5 * time.Millisecond
	if r.global != nil {
		if ok, retry := tokenAllow(r.global, tolerableDelay); !ok {
			return false, retry
		}
	}
	if r.perOwnerR > 0 {
		key := owner.String()
		r.mu.RLock()
		lim, ok := r.perOwner[key]
		r.mu.RUnlock()
		if !ok {
			r.mu.Lock()
			if lim, ok = r.perOwner[key]; !ok {
				burst := r.perOwnerB
				if burst <= 0 {
					burst = int(r.perOwnerR)
					if burst < 1 {
						burst = 1
					}
				}
				lim = rate.NewLimiter(r.perOwnerR, burst)
				r.perOwner[key] = lim
			}
			r.mu.Unlock()
		}
		if ok, retry := tokenAllow(lim, tolerableDelay); !ok {
			return false, retry
		}
	}
	return true, 0
}

// tokenAllow is the shared Reserve+tolerance helper.
func tokenAllow(lim *rate.Limiter, tolerable time.Duration) (bool, time.Duration) {
	now := time.Now()
	res := lim.Reserve()
	if !res.OK() {
		return false, time.Second
	}
	d := res.DelayFrom(now)
	if d <= tolerable {
		return true, 0
	}
	res.Cancel()
	return false, d
}
