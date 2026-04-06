package rest

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

const (
	contextKeyRole   contextKey = "role"
	contextKeyAPIKey contextKey = "apiKey"
)

// roleFromContext extracts the role from the request context.
func roleFromContext(ctx context.Context) string {
	v, _ := ctx.Value(contextKeyRole).(string)
	return v
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
			if os.Getenv("CHATCLI_OPERATOR_DEV_MODE") == "true" {
				ctx := context.WithValue(r.Context(), contextKeyRole, "admin")
				ctx = context.WithValue(ctx, contextKeyAPIKey, "dev-mode")
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
		s.apiKeysMu.RUnlock()
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		ctx := context.WithValue(r.Context(), contextKeyRole, role)
		ctx = context.WithValue(ctx, contextKeyAPIKey, apiKey)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireRole returns a middleware that checks the role meets the minimum requirement.
// Role hierarchy: admin > operator > viewer.
func requireRole(minRole string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := roleFromContext(r.Context())
		if !hasMinRole(role, minRole) {
			writeError(w, http.StatusForbidden, "insufficient permissions: requires "+minRole+" role")
			return
		}
		next.ServeHTTP(w, r)
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

// rateLimiter holds per-key token buckets.
type rateLimiter struct {
	buckets sync.Map // map[string]*tokenBucket
	maxRPM  float64  // requests per minute
}

// newRateLimiter creates a rate limiter with the given max requests per minute.
func newRateLimiter(maxRPM float64) *rateLimiter {
	return &rateLimiter{maxRPM: maxRPM}
}

// getBucket returns (or creates) the token bucket for the given key.
func (rl *rateLimiter) getBucket(key string) *tokenBucket {
	if v, ok := rl.buckets.Load(key); ok {
		return v.(*tokenBucket)
	}
	tb := newTokenBucket(rl.maxRPM, rl.maxRPM/60.0) // refill at rate per second
	actual, _ := rl.buckets.LoadOrStore(key, tb)
	return actual.(*tokenBucket)
}

// rateLimitMiddleware enforces rate limiting per API key.
func (s *APIServer) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, _ := r.Context().Value(contextKeyAPIKey).(string)
		if key == "" {
			key = r.RemoteAddr
		}

		bucket := s.limiter.getBucket(key)
		if !bucket.allow() {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded: 100 requests per minute")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// --- CORS Middleware ---

// corsMiddleware adds CORS headers to responses.
// Security (H6): Default to deny-all CORS. Require explicit origin configuration.
func (s *APIServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := s.corsOrigin
		// Security: deny-all by default — CORS only if explicitly configured
		if origin == "" {
			// No CORS headers set — browser cross-origin requests will be blocked
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+s.apiKeyHeader+", Authorization")
		w.Header().Set("Access-Control-Allow-Credentials", "false")
		w.Header().Set("Access-Control-Max-Age", "3600") // 1 hour, not 24h

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
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
		log.Printf("[REST] %s %s %d %s role=%s",
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

// matchRoute checks if segments match a pattern like ["api", "v1", "incidents", ":name"].
// Returns extracted parameters or nil if no match.
func matchRoute(segments []string, pattern []string) map[string]string {
	if len(segments) != len(pattern) {
		return nil
	}
	params := make(map[string]string)
	for i, p := range pattern {
		if strings.HasPrefix(p, ":") {
			params[p[1:]] = segments[i]
		} else if p != segments[i] {
			return nil
		}
	}
	return params
}
