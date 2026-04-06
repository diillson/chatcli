/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// RateLimiterConfig holds rate limiter configuration.
type RateLimiterConfig struct {
	// RequestsPerSecond is the sustained rate limit per client.
	RequestsPerSecond float64
	// Burst is the maximum number of requests allowed in a burst.
	Burst int
	// CleanupInterval is how often idle client limiters are evicted.
	CleanupInterval time.Duration
	// MaxIdleTime is how long a limiter can be idle before eviction.
	MaxIdleTime time.Duration
}

// DefaultRateLimiterConfig returns production-safe defaults.
// Override via CHATCLI_RATE_LIMIT_RPS and CHATCLI_RATE_LIMIT_BURST env vars.
func DefaultRateLimiterConfig() RateLimiterConfig {
	rps := 10.0
	burst := 30

	if v := os.Getenv("CHATCLI_RATE_LIMIT_RPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			rps = f
		}
	}
	if v := os.Getenv("CHATCLI_RATE_LIMIT_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			burst = n
		}
	}

	return RateLimiterConfig{
		RequestsPerSecond: rps,
		Burst:             burst,
		CleanupInterval:   5 * time.Minute,
		MaxIdleTime:       10 * time.Minute,
	}
}

type clientLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// PerClientRateLimiter provides per-client token-bucket rate limiting for gRPC.
type PerClientRateLimiter struct {
	mu       sync.Mutex
	clients  map[string]*clientLimiter
	config   RateLimiterConfig
	logger   *zap.Logger
	stopChan chan struct{}
}

// NewPerClientRateLimiter creates a rate limiter and starts background cleanup.
func NewPerClientRateLimiter(cfg RateLimiterConfig, logger *zap.Logger) *PerClientRateLimiter {
	rl := &PerClientRateLimiter{
		clients:  make(map[string]*clientLimiter),
		config:   cfg,
		logger:   logger.Named("rate_limiter"),
		stopChan: make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

// UnaryInterceptor returns a gRPC unary interceptor that enforces rate limits.
func (rl *PerClientRateLimiter) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		clientID := rl.extractClientID(ctx)
		limiter := rl.getLimiter(clientID)

		if !limiter.Allow() {
			rl.logger.Warn("rate limit exceeded",
				zap.String("client", clientID),
				zap.String("method", info.FullMethod),
			)
			// Set Retry-After header
			retryAfter := fmt.Sprintf("%.0f", 1.0/rl.config.RequestsPerSecond)
			_ = grpc.SetHeader(ctx, metadata.Pairs("retry-after", retryAfter))
			return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded, retry after %s seconds", retryAfter)
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor returns a gRPC stream interceptor that enforces rate limits.
func (rl *PerClientRateLimiter) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		clientID := rl.extractClientID(ss.Context())
		limiter := rl.getLimiter(clientID)

		if !limiter.Allow() {
			rl.logger.Warn("rate limit exceeded (stream)",
				zap.String("client", clientID),
				zap.String("method", info.FullMethod),
			)
			return status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
		}
		return handler(srv, ss)
	}
}

// Stop shuts down the background cleanup goroutine.
func (rl *PerClientRateLimiter) Stop() {
	close(rl.stopChan)
}

func (rl *PerClientRateLimiter) extractClientID(ctx context.Context) string {
	// Prefer user identity from JWT/auth context
	if user := UserFromContext(ctx); user != nil {
		return user.Subject
	}
	// Fall back to peer address
	if p, ok := peer.FromContext(ctx); ok {
		return p.Addr.String()
	}
	return "unknown"
}

func (rl *PerClientRateLimiter) getLimiter(clientID string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cl, exists := rl.clients[clientID]
	if !exists {
		cl = &clientLimiter{
			limiter:  rate.NewLimiter(rate.Limit(rl.config.RequestsPerSecond), rl.config.Burst),
			lastSeen: time.Now(),
		}
		rl.clients[clientID] = cl
	} else {
		cl.lastSeen = time.Now()
	}
	return cl.limiter
}

func (rl *PerClientRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.config.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now()
			for id, cl := range rl.clients {
				if now.Sub(cl.lastSeen) > rl.config.MaxIdleTime {
					delete(rl.clients, id)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopChan:
			return
		}
	}
}
