package rest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const (
	contextKeyRole     contextKey = "role"
	contextKeyAPIKey   contextKey = "apiKey"
	contextKeyIdentity contextKey = "identity"

	// devModeIdentity is the identity of every caller in dev mode (no keys).
	devModeIdentity = "dev-mode"
)

// DevModeEnvVar names the switch that admits every REST caller as admin
// while no API key is configured.
const DevModeEnvVar = "CHATCLI_OPERATOR_DEV_MODE"

// DevModeEnabled reports whether dev mode is on. It is the one parse of
// CHATCLI_OPERATOR_DEV_MODE: the auth middleware, the rate limiter and the
// startup log all ask it, so the log never announces a mode the API does
// not apply. The value follows strconv.ParseBool, case-insensitively and
// ignoring surrounding spaces ("true", "True", "1", "t"); anything else,
// unset included, is off.
func DevModeEnabled() bool {
	on, err := strconv.ParseBool(strings.ToLower(strings.TrimSpace(os.Getenv(DevModeEnvVar))))
	return err == nil && on
}

// roleFromContext extracts the role from the request context.
func roleFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyRole).(string)
	return v
}

// identityFromContext returns the identity of the API key that
// authenticated the request: the key's name (or description) from the key
// list, or a fingerprint of the key when it has neither.
func identityFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyIdentity).(string)
	return v
}

// APIKey is one configured REST API key.
type APIKey struct {
	// Role is viewer, operator or admin.
	Role string
	// Name identifies the key's holder on the approval decisions it takes.
	// Empty falls back to a fingerprint of the key.
	Name string
}

// SetAPIKeyEntries configures API keys together with the identity each one
// records on approval decisions. It replaces every key; an empty map
// leaves the API without keys, which rejects every call unless dev mode is
// on. Thread-safe.
func (s *APIServer) SetAPIKeyEntries(keys map[string]APIKey) {
	roles := make(map[string]string, len(keys))
	names := make(map[string]string, len(keys))
	for k, v := range keys {
		roles[k] = v.Role
		if n := strings.TrimSpace(v.Name); n != "" {
			names[k] = n
		}
	}
	s.apiKeysMu.Lock()
	defer s.apiKeysMu.Unlock()
	s.apiKeys = roles
	s.apiKeyNames = names
}

// APIKeyCount reports how many API keys are loaded.
func (s *APIServer) APIKeyCount() int {
	s.apiKeysMu.RLock()
	defer s.apiKeysMu.RUnlock()
	return len(s.apiKeys)
}

// keyFingerprint identifies a key without revealing it.
func keyFingerprint(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return "key-" + hex.EncodeToString(sum[:])[:12]
}

// --- Authentication Middleware ---

// authMiddleware checks the X-API-Key header and maps to a role.
func (s *APIServer) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.apiKeysMu.RLock()
		keysLen := len(s.apiKeys)
		s.apiKeysMu.RUnlock()

		// Security (C4): Fail-closed. If no keys configured, reject unless dev mode.
		if keysLen == 0 {
			if DevModeEnabled() {
				ctx := context.WithValue(r.Context(), contextKeyRole, "admin")
				ctx = context.WithValue(ctx, contextKeyAPIKey, "dev-mode")
				ctx = context.WithValue(ctx, contextKeyIdentity, devModeIdentity)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			writeError(w, http.StatusUnauthorized, "no API keys configured; set CHATCLI_OPERATOR_DEV_MODE=true for development")
			return
		}

		apiKey := r.Header.Get(s.apiKeyHeader)
		if apiKey == "" {
			writeError(w, http.StatusUnauthorized, "missing API key in "+s.apiKeyHeader+" header")
			return
		}

		// Find the role for this key.
		s.apiKeysMu.RLock()
		role, ok := s.apiKeys[apiKey]
		identity := s.apiKeyNames[apiKey]
		s.apiKeysMu.RUnlock()
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}
		if identity == "" {
			identity = keyFingerprint(apiKey)
		}

		ctx := context.WithValue(r.Context(), contextKeyRole, role)
		ctx = context.WithValue(ctx, contextKeyAPIKey, apiKey)
		ctx = context.WithValue(ctx, contextKeyIdentity, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// hasMinRole checks if the given role meets the minimum role requirement.
func hasMinRole(role, minRole string) bool {
	roleLevel := map[string]int{
		"viewer":   1,
		"operator": 2,
		"admin":    3,
	}
	return roleLevel[role] >= roleLevel[minRole]
}

// --- Rate Limiting Middleware ---

// tokenBucket implements a simple token bucket rate limiter.
type tokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex
}

// newTokenBucket creates a token bucket with the given max tokens and refill rate.
func newTokenBucket(maxTokens, refillRate float64) *tokenBucket {
	return &tokenBucket{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// allow checks if a request is allowed and consumes a token if so.
func (tb *tokenBucket) allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.maxTokens {
		tb.tokens = tb.maxTokens
	}
	tb.lastRefill = now

	if tb.tokens < 1.0 {
		return false
	}
	tb.tokens--
	return true
}

// Rate limits of the REST API, in requests per minute.
const (
	// unauthenticatedRequestsPerMinute bounds, per client host, requests
	// that carry no valid API key: the surface a client guessing keys
	// works on.
	unauthenticatedRequestsPerMinute = 30
	// authenticatedRequestsPerMinute bounds requests per API key. The
	// dashboard issues about a dozen requests per refresh and viewer (up
	// to 66 a minute at its fastest refresh), so the ceiling leaves room
	// for several viewers sharing one key.
	authenticatedRequestsPerMinute = 600
)

// Idle bucket eviction. A bucket idle long enough to have refilled is
// indistinguishable from a new one, so dropping it loses nothing.
const (
	bucketPruneInterval = 5 * time.Minute
	bucketIdleTTL       = 10 * time.Minute
)

// rateLimiter holds per-key token buckets.
type rateLimiter struct {
	buckets sync.Map // map[string]*tokenBucket
	maxRPM  float64  // requests per minute

	pruneMu   sync.Mutex
	lastPrune time.Time
}

// newRateLimiter creates a rate limiter with the given max requests per minute.
func newRateLimiter(maxRPM float64) *rateLimiter {
	return &rateLimiter{maxRPM: maxRPM, lastPrune: time.Now()}
}

// getBucket returns (or creates) the token bucket for the given key.
func (rl *rateLimiter) getBucket(key string) *tokenBucket {
	rl.pruneIdle(time.Now())
	if v, ok := rl.buckets.Load(key); ok {
		return v.(*tokenBucket)
	}
	tb := newTokenBucket(rl.maxRPM, rl.maxRPM/60.0) // refill at rate per second
	actual, _ := rl.buckets.LoadOrStore(key, tb)
	return actual.(*tokenBucket)
}

// pruneIdle drops buckets unused for bucketIdleTTL, at most once per
// bucketPruneInterval, so the map follows the set of active clients
// instead of growing with every client ever seen.
func (rl *rateLimiter) pruneIdle(now time.Time) {
	rl.pruneMu.Lock()
	if now.Sub(rl.lastPrune) < bucketPruneInterval {
		rl.pruneMu.Unlock()
		return
	}
	rl.lastPrune = now
	rl.pruneMu.Unlock()

	rl.buckets.Range(func(k, v any) bool {
		tb := v.(*tokenBucket)
		tb.mu.Lock()
		idle := now.Sub(tb.lastRefill)
		tb.mu.Unlock()
		if idle >= bucketIdleTTL {
			rl.buckets.Delete(k)
		}
		return true
	})
}

// exceededMessage is the 429 body, derived from the configured limit.
func (rl *rateLimiter) exceededMessage() string {
	return fmt.Sprintf("rate limit exceeded: %d requests per minute", int(rl.maxRPM))
}

// retryAfterSeconds is how long until the bucket holds a token again.
func (rl *rateLimiter) retryAfterSeconds() int {
	if rl.maxRPM <= 0 {
		return 60
	}
	return max(1, int(math.Ceil(60/rl.maxRPM)))
}

// clientHost is the host part of the peer address. Keying by the full
// address (host and ephemeral port) gives every new TCP connection a fresh
// bucket, which is no limit at all. Forwarding headers are not honored:
// with no trusted-proxy configuration, anything in them is client input.
func clientHost(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// rateLimitMiddleware runs before authentication, so it must not trust the
// key it has not verified yet. A request with a valid API key is limited
// per key (a digest of it, never the key itself); anything else is
// limited per client host, which is what bounds key guessing.
func (s *APIServer) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limiter, key := s.rateLimitBucketFor(r)
		bucket := limiter.getBucket(key)
		if !bucket.allow() {
			w.Header().Set("Retry-After", strconv.Itoa(limiter.retryAfterSeconds()))
			writeError(w, http.StatusTooManyRequests, limiter.exceededMessage())
			return
		}

		next.ServeHTTP(w, r)
	})
}

// rateLimitBucketFor picks the limiter and bucket key for a request.
func (s *APIServer) rateLimitBucketFor(r *http.Request) (*rateLimiter, string) {
	s.apiKeysMu.RLock()
	keysLen := len(s.apiKeys)
	_, valid := s.apiKeys[r.Header.Get(s.apiKeyHeader)]
	s.apiKeysMu.RUnlock()

	switch {
	case valid && keysLen > 0:
		sum := sha256.Sum256([]byte(r.Header.Get(s.apiKeyHeader)))
		return s.keyLimiter, "key:" + hex.EncodeToString(sum[:])
	case keysLen == 0 && DevModeEnabled():
		// Dev mode admits every caller as admin; limit it like a key.
		return s.keyLimiter, "dev:" + clientHost(r)
	default:
		return s.limiter, "host:" + clientHost(r)
	}
}

// --- CORS Middleware ---

// corsMiddleware adds CORS headers to responses.
// Security (H6): Default to deny-all CORS. Require explicit origin configuration.
func (s *APIServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deny-all until an origin is configured: with no policy, no CORS
		// headers are written and a browser blocks the cross-origin call.
		s.corsMu.RLock()
		policy := s.corsPolicy
		s.corsMu.RUnlock()

		if policy.apply(w, r, s.apiKeyHeader) {
			return // preflight answered
		}
		next.ServeHTTP(w, r)
	})
}

// --- Logging Middleware ---

// loggingMiddleware logs each request with method, path, status, and duration.
type statusCapture struct {
	http.ResponseWriter
	statusCode int
}

func (sc *statusCapture) WriteHeader(code int) {
	sc.statusCode = code
	sc.ResponseWriter.WriteHeader(code)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sc := &statusCapture{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(sc, r)

		duration := time.Since(start)
		role := roleFromContext(r.Context())
		if role == "" {
			role = "-"
		}
		// %q on the request-controlled fields escapes control characters so
		// a crafted path cannot forge extra log lines. gosec's taint pass
		// has no sanitizer model for fmt quoting, hence the annotation.
		log.Printf("[REST] %q %q %d %s role=%s", // #nosec G706 -- method and path are %q-escaped above
			r.Method, r.URL.Path, sc.statusCode, duration.Round(time.Microsecond), role)
	})
}

// --- Chain helper ---

// chain applies middleware in order: the first middleware wraps the outermost layer.
func chain(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}

// --- Path parameter extraction ---

// pathSegments splits a URL path into segments, stripping the leading "/".
func pathSegments(path string) []string {
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}
