/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"crypto/subtle"
	"os"
	"strings"
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

// TokenAuthInterceptor validates Bearer tokens from gRPC metadata.
// Supports both legacy shared token and JWT-based authentication.
type TokenAuthInterceptor struct {
	token  string
	logger *zap.Logger

	// JWT configuration (optional, loaded from env)
	jwtSecret []byte // from CHATCLI_JWT_SECRET

	// Auth failure rate limiting: max 5 failures/min per IP
	failureMu       sync.Mutex
	failureLimiters map[string]*rate.Limiter
}

// NewTokenAuthInterceptor creates a new auth interceptor.
// If token is empty and no JWT config is set, authentication is disabled.
// JWT is configured via CHATCLI_JWT_SECRET environment variable.
func NewTokenAuthInterceptor(token string, logger *zap.Logger) *TokenAuthInterceptor {
	ai := &TokenAuthInterceptor{
		token:           token,
		logger:          logger,
		failureLimiters: make(map[string]*rate.Limiter),
	}

	// Load JWT secret from environment if available
	if secret := os.Getenv("CHATCLI_JWT_SECRET"); secret != "" {
		ai.jwtSecret = []byte(secret)
		logger.Info("JWT authentication enabled (HS256)")
	}

	// Start background cleanup to prevent memory leak from auth failure limiters
	ai.startFailureLimiterCleanup()

	return ai
}

// Unary returns a grpc.UnaryServerInterceptor that validates credentials and
// injects UserInfo into the context for downstream access control.
func (a *TokenAuthInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		newCtx, err := a.authorize(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// Stream returns a grpc.StreamServerInterceptor that validates credentials.
func (a *TokenAuthInterceptor) Stream() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		newCtx, err := a.authorize(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		// Wrap the stream with the new context containing UserInfo
		wrapped := &wrappedServerStream{ServerStream: ss, ctx: newCtx}
		return handler(srv, wrapped)
	}
}

// wrappedServerStream wraps a grpc.ServerStream to override the context.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

// authorize validates credentials and returns a context with UserInfo on success.
func (a *TokenAuthInterceptor) authorize(ctx context.Context, method string) (context.Context, error) {
	// Skip auth if no token and no JWT configured
	if a.token == "" && a.jwtSecret == nil {
		// No auth configured — inject default admin user for backward compat
		return ContextWithUser(ctx, &UserInfo{Subject: "system", Role: RoleAdmin}), nil
	}

	// Always allow health checks without auth
	if strings.HasSuffix(method, "/Health") {
		return ctx, nil
	}

	// Check auth failure rate limiting
	peerAddr := extractPeerAddress(ctx)
	if !a.checkFailureRate(peerAddr) {
		a.logger.Warn("auth failure rate limit exceeded", zap.String("peer", peerAddr))
		return ctx, status.Errorf(codes.Unauthenticated, "authentication failed")
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		a.recordFailure(peerAddr)
		a.logger.Warn("missing metadata", zap.String("method", method))
		return ctx, status.Errorf(codes.Unauthenticated, "authentication failed")
	}

	values := md.Get("authorization")
	if len(values) == 0 {
		a.recordFailure(peerAddr)
		return ctx, status.Errorf(codes.Unauthenticated, "authentication failed")
	}

	token := values[0]
	if !strings.HasPrefix(token, "Bearer ") {
		a.recordFailure(peerAddr)
		return ctx, status.Errorf(codes.Unauthenticated, "authentication failed")
	}

	token = strings.TrimPrefix(token, "Bearer ")

	// Try JWT validation first if configured
	if a.jwtSecret != nil {
		if user, err := a.validateJWT(token); err == nil {
			return ContextWithUser(ctx, user), nil
		}
		// JWT validation failed — fall through to legacy token check
	}

	// Legacy shared token validation
	if a.token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(a.token)) == 1 {
		// Legacy token — assign default admin role
		return ContextWithUser(ctx, &UserInfo{
			Subject: "legacy-token",
			Role:    RoleAdmin,
		}), nil
	}

	a.recordFailure(peerAddr)
	a.logger.Warn("authentication failed", zap.String("method", method), zap.String("peer", peerAddr))
	// Return same error code for all auth failures — no info leakage (M8)
	return ctx, status.Errorf(codes.Unauthenticated, "authentication failed")
}

// jwtClockSkew is the tolerance for token expiry checks (handles clock drift).
const jwtClockSkew = 30 // seconds

// validateJWT parses and validates a JWT token, extracting user identity.
// Validates: signature (HS256), expiry (with clock skew), audience, and issuer.
func (a *TokenAuthInterceptor) validateJWT(tokenStr string) (*UserInfo, error) {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return nil, status.Errorf(codes.Unauthenticated, "invalid token format")
	}

	// Base64url → standard base64 replacer
	b64URLReplacer := strings.NewReplacer("-", "+", "_", "/")

	// Verify HMAC-SHA256 signature
	expectedSig := computeHS256(parts[0]+"."+parts[1], a.jwtSecret)
	sigB64 := padBase64(b64URLReplacer.Replace(parts[2]))

	actualSig, err := base64Decode(sigB64)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed")
	}

	if subtle.ConstantTimeCompare(expectedSig, actualSig) != 1 {
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed")
	}

	// Decode payload
	payloadB64 := padBase64(b64URLReplacer.Replace(parts[1]))
	payloadBytes, err := base64Decode(payloadB64)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed")
	}

	claims, err := parseJSONClaims(payloadBytes)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "authentication failed")
	}

	now := time.Now().Unix()

	// Check expiry with clock skew tolerance
	if exp, ok := claims["exp"]; ok {
		if expFloat, ok := exp.(float64); ok {
			if now > int64(expFloat)+jwtClockSkew {
				return nil, status.Errorf(codes.Unauthenticated, "token expired")
			}
		}
	}

	// Check not-before with clock skew tolerance
	if nbf, ok := claims["nbf"]; ok {
		if nbfFloat, ok := nbf.(float64); ok {
			if now < int64(nbfFloat)-jwtClockSkew {
				return nil, status.Errorf(codes.Unauthenticated, "token not yet valid")
			}
		}
	}

	// Validate issuer if configured (CHATCLI_JWT_ISSUER)
	if expectedIss := os.Getenv("CHATCLI_JWT_ISSUER"); expectedIss != "" {
		if iss := getStringClaim(claims, "iss"); iss != expectedIss {
			return nil, status.Errorf(codes.Unauthenticated, "authentication failed")
		}
	}

	// Validate audience if configured (CHATCLI_JWT_AUDIENCE)
	if expectedAud := os.Getenv("CHATCLI_JWT_AUDIENCE"); expectedAud != "" {
		aud := getStringClaim(claims, "aud")
		if aud != expectedAud {
			return nil, status.Errorf(codes.Unauthenticated, "authentication failed")
		}
	}

	user := &UserInfo{
		Subject: getStringClaim(claims, "sub"),
		Role:    ParseRole(getStringClaim(claims, "role")),
		Email:   getStringClaim(claims, "email"),
	}
	if tid := getStringClaim(claims, "tenant_id"); tid != "" {
		user.TenantID = tid
	}
	if user.Subject == "" {
		user.Subject = "jwt-user"
	}

	return user, nil
}

// padBase64 adds standard base64 padding if necessary.
func padBase64(s string) string {
	switch len(s) % 4 {
	case 2:
		return s + "=="
	case 3:
		return s + "="
	}
	return s
}

// startFailureLimiterCleanup evicts stale auth failure limiters every 5 minutes.
func (a *TokenAuthInterceptor) startFailureLimiterCleanup() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			a.failureMu.Lock()
			// Evict all limiters — they'll be recreated on next failure.
			// This bounds memory to O(unique IPs in last 5 minutes).
			if len(a.failureLimiters) > 0 {
				a.failureLimiters = make(map[string]*rate.Limiter)
			}
			a.failureMu.Unlock()
		}
	}()
}

// checkFailureRate returns true if the client hasn't exceeded the auth failure limit.
func (a *TokenAuthInterceptor) checkFailureRate(peerAddr string) bool {
	a.failureMu.Lock()
	defer a.failureMu.Unlock()

	l, exists := a.failureLimiters[peerAddr]
	if !exists {
		// 5 failures per minute (1 every 12 seconds, burst 5)
		l = rate.NewLimiter(rate.Every(12*time.Second), 5)
		a.failureLimiters[peerAddr] = l
	}
	return l.Allow()
}

// recordFailure consumes a token from the failure rate limiter.
func (a *TokenAuthInterceptor) recordFailure(peerAddr string) {
	// The failure is already recorded by the Allow() call in checkFailureRate
	// This is a no-op placeholder for additional failure tracking (metrics, alerting)
}

func extractPeerAddress(ctx context.Context) string {
	if p, ok := peer.FromContext(ctx); ok {
		return p.Addr.String()
	}
	return "unknown"
}
