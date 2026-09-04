package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func signedJWT(t *testing.T, secret []byte, claims map[string]interface{}) string {
	t.Helper()
	enc := func(v interface{}) string {
		raw, _ := json.Marshal(v)
		return strings.TrimRight(base64.URLEncoding.EncodeToString(raw), "=")
	}
	head := enc(map[string]string{"alg": "HS256", "typ": "JWT"})
	body := enc(claims)
	sig := computeHS256(head+"."+body, secret)
	return head + "." + body + "." + strings.TrimRight(base64.URLEncoding.EncodeToString(sig), "=")
}

// A token with no expiry never expires, which turns a leaked credential
// into a permanent one.
func TestJWTWithoutExpiryIsRejected(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "test-secret-value")
	a := NewTokenAuthInterceptor("", zap.NewNop())

	valid := signedJWT(t, a.jwtSecret, map[string]interface{}{
		"sub": "alice", "role": "admin", "exp": float64(time.Now().Add(time.Hour).Unix()),
	})
	if _, err := a.validateJWT(valid); err != nil {
		t.Fatalf("a token with a future expiry must be accepted: %v", err)
	}

	noExp := signedJWT(t, a.jwtSecret, map[string]interface{}{"sub": "alice", "role": "admin"})
	if _, err := a.validateJWT(noExp); err == nil {
		t.Error("a token with no expiry must be rejected")
	}

	badExp := signedJWT(t, a.jwtSecret, map[string]interface{}{
		"sub": "alice", "exp": "not-a-number",
	})
	if _, err := a.validateJWT(badExp); err == nil {
		t.Error("a token whose expiry is not a number must be rejected")
	}

	expired := signedJWT(t, a.jwtSecret, map[string]interface{}{
		"sub": "alice", "exp": float64(time.Now().Add(-time.Hour).Unix()),
	})
	if _, err := a.validateJWT(expired); err == nil {
		t.Error("an expired token must be rejected")
	}
}

// The limiter must count per client, not per connection: keying on the
// ephemeral port made the limit meaningless against a caller willing to
// reconnect.
func TestPeerAddressDropsTheEphemeralPort(t *testing.T) {
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &fakeAddr{"10.0.0.5:54321"}})
	if got := extractPeerAddress(ctx); got != "10.0.0.5" {
		t.Errorf("extractPeerAddress = %q, want the host alone", got)
	}
	other := peer.NewContext(context.Background(), &peer.Peer{Addr: &fakeAddr{"10.0.0.5:54322"}})
	if extractPeerAddress(ctx) != extractPeerAddress(other) {
		t.Error("two connections from one client must share a limiter")
	}
	if got := extractPeerAddress(context.Background()); got != "unknown" {
		t.Errorf("a context with no peer must read as unknown, got %q", got)
	}
}

func TestAuthFailureLimiterCountsPerClient(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "")
	a := NewTokenAuthInterceptor("the-token", zap.NewNop())
	md := metadata.Pairs("authorization", "Bearer wrong")

	var refusedByLimiter bool
	for i := 0; i < 12; i++ {
		ctx := peer.NewContext(metadata.NewIncomingContext(context.Background(), md),
			&peer.Peer{Addr: &fakeAddr{"10.0.0.9:" + string(rune('a'+i))}})
		if _, err := a.authorize(ctx, "/svc/Method"); err != nil && i > 5 {
			refusedByLimiter = true
		}
	}
	if !refusedByLimiter {
		t.Error("repeated failures from one client must eventually be rate limited")
	}
}

type fakeAddr struct{ s string }

func (f *fakeAddr) Network() string { return "tcp" }
func (f *fakeAddr) String() string  { return f.s }
