/*
 * Package credpool provides multi-key credential rotation per provider.
 *
 * SecOps motivation: a single API key is a single point of failure and a
 * single rate-limit ceiling. Operators can supply several keys (e.g.
 * OPENAI_API_KEYS="k1,k2,k3"); the pool hands out a healthy key per request
 * and parks a key on a cooldown when it returns an auth/quota error, then
 * brings it back automatically. With exactly one key the pool is a
 * transparent pass-through, so existing single-key setups behave identically
 * (no regression).
 *
 * Keys are never logged. Fingerprint() yields a short, non-reversible label
 * for diagnostics.
 */
package credpool

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Strategy selects which healthy key to return next.
type Strategy int

const (
	// FillFirst always returns the first healthy key (rotate only on
	// failure). Best when keys have separate quotas and you want to drain
	// one before touching the next.
	FillFirst Strategy = iota
	// RoundRobin spreads load across all healthy keys.
	RoundRobin
)

// DefaultCooldown is how long an exhausted key is parked before retry.
const DefaultCooldown = 60 * time.Second

type credential struct {
	key            string
	exhaustedUntil time.Time
	failures       int
}

// Pool is a thread-safe set of interchangeable credentials for one provider.
type Pool struct {
	mu       sync.Mutex
	creds    []*credential
	strategy Strategy
	cursor   int
	cooldown time.Duration
	now      func() time.Time // injectable clock for tests
}

// ParseKeys splits a raw multi-key string on commas, semicolons, newlines and
// whitespace, trims, and de-duplicates while preserving order.
func ParseKeys(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	seen := map[string]struct{}{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		k := strings.TrimSpace(f)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// New builds a pool from keys. cooldown <= 0 uses DefaultCooldown.
func New(keys []string, strategy Strategy, cooldown time.Duration) *Pool {
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	creds := make([]*credential, 0, len(keys))
	for _, k := range keys {
		creds = append(creds, &credential{key: k})
	}
	return &Pool{creds: creds, strategy: strategy, cooldown: cooldown, now: time.Now}
}

// Size returns the total number of keys.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.creds)
}

// Available returns how many keys are currently healthy (not in cooldown).
func (p *Pool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	n := 0
	for _, c := range p.creds {
		if c.exhaustedUntil.IsZero() || now.After(c.exhaustedUntil) {
			n++
		}
	}
	return n
}

// Next returns the next healthy key. If every key is in cooldown it returns
// the one whose cooldown expires soonest (better to try a likely-throttled
// key than to fail outright). ok is false only when the pool is empty.
func (p *Pool) Next() (key string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.creds) == 0 {
		return "", false
	}
	now := p.now()

	n := len(p.creds)
	switch p.strategy {
	case RoundRobin:
		for i := 0; i < n; i++ {
			idx := (p.cursor + i) % n
			c := p.creds[idx]
			if c.exhaustedUntil.IsZero() || now.After(c.exhaustedUntil) {
				p.cursor = (idx + 1) % n
				return c.key, true
			}
		}
	default: // FillFirst
		for i := 0; i < n; i++ {
			c := p.creds[i]
			if c.exhaustedUntil.IsZero() || now.After(c.exhaustedUntil) {
				return c.key, true
			}
		}
	}

	// All parked: return the soonest-to-recover key.
	soonest := p.creds[0]
	for _, c := range p.creds[1:] {
		if c.exhaustedUntil.Before(soonest.exhaustedUntil) {
			soonest = c
		}
	}
	return soonest.key, true
}

// MarkExhausted parks a key for the cooldown window (auth/quota failure).
func (p *Pool) MarkExhausted(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.creds {
		if c.key == key {
			c.failures++
			c.exhaustedUntil = p.now().Add(p.cooldown)
			return
		}
	}
}

// MarkOK clears a key's failure state after a successful call.
func (p *Pool) MarkOK(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.creds {
		if c.key == key {
			c.failures = 0
			c.exhaustedUntil = time.Time{}
			return
		}
	}
}

// Fingerprint returns a short, non-reversible label for a key, safe to log.
func Fingerprint(key string) string {
	if key == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(key))
	return "key:" + hex.EncodeToString(sum[:3])
}

// --- process-global registry, one pool per provider ---

var (
	regMu sync.RWMutex
	pools = map[string]*Pool{}
)

// Register installs a pool for a provider (replacing any existing one).
func Register(provider string, p *Pool) {
	regMu.Lock()
	pools[provider] = p
	regMu.Unlock()
}

// For returns the registered pool for a provider, if any.
func For(provider string) (*Pool, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := pools[provider]
	return p, ok
}

// ResetRegistry clears all registered pools (tests).
func ResetRegistry() {
	regMu.Lock()
	pools = map[string]*Pool{}
	regMu.Unlock()
}
