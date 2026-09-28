/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// The retry-after hint is the refill time of one token rounded up, never 0:
// the old %.0f of 1/rps printed "0" for every rate above 2 rps (the default
// is 10), telling a limited client to retry at once.
func TestRetryAfterSeconds(t *testing.T) {
	cases := []struct {
		rps  float64
		want int
	}{
		{10, 1},
		{2.5, 1},
		{1, 1},
		{0.5, 2},
		{0.3, 4},
		{0, 1},
		{-1, 1},
		{math.Inf(1), 1},
		{math.NaN(), 1},
		{1e-12, math.MaxInt32},
	}
	for _, tc := range cases {
		if got := retryAfterSeconds(tc.rps); got != tc.want {
			t.Errorf("retryAfterSeconds(%v) = %d, want %d", tc.rps, got, tc.want)
		}
	}
}

func TestUnaryRateLimitNamesRealRetryAfter(t *testing.T) {
	rl := NewPerClientRateLimiter(RateLimiterConfig{RequestsPerSecond: 0.5, Burst: 1, CleanupInterval: time.Minute, MaxIdleTime: time.Minute}, zap.NewNop())
	t.Cleanup(rl.Stop)
	intercept := rl.UnaryInterceptor()
	handler := func(context.Context, interface{}) (interface{}, error) { return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/chatcli.v1.ChatCLIService/Health"}

	if _, err := intercept(context.Background(), nil, info, handler); err != nil {
		t.Fatalf("first call inside the burst: %v", err)
	}
	_, err := intercept(context.Background(), nil, info, handler)
	if err == nil || !strings.Contains(err.Error(), "retry after 2 seconds") {
		t.Fatalf("limited call: got %v, want a 2 second retry-after", err)
	}
}

// headerRecordingStream carries a context and records the headers an
// interceptor sets on it.
type headerRecordingStream struct {
	grpc.ServerStream
	ctx     context.Context
	headers metadata.MD
}

func (s *headerRecordingStream) Context() context.Context { return s.ctx }
func (s *headerRecordingStream) SetHeader(md metadata.MD) error {
	s.headers = metadata.Join(s.headers, md)
	return nil
}

// The stream interceptor used to reject with no retry hint at all; it now
// sets the same retry-after header and names the seconds in the status.
func TestStreamRateLimitNamesRealRetryAfter(t *testing.T) {
	rl := NewPerClientRateLimiter(RateLimiterConfig{RequestsPerSecond: 0.5, Burst: 1, CleanupInterval: time.Minute, MaxIdleTime: time.Minute}, zap.NewNop())
	t.Cleanup(rl.Stop)
	intercept := rl.StreamInterceptor()
	handler := func(interface{}, grpc.ServerStream) error { return nil }
	info := &grpc.StreamServerInfo{FullMethod: "/chatcli.v1.ChatCLIService/StreamPrompt"}
	ctx := ContextWithUser(context.Background(), &UserInfo{Subject: "stream-caller", Role: RoleUser})

	if err := intercept(nil, &headerRecordingStream{ctx: ctx}, info, handler); err != nil {
		t.Fatalf("first call inside the burst: %v", err)
	}
	ss := &headerRecordingStream{ctx: ctx}
	err := intercept(nil, ss, info, handler)
	if err == nil || !strings.Contains(err.Error(), "retry after 2 seconds") {
		t.Fatalf("limited call: got %v, want a 2 second retry-after", err)
	}
	if got := ss.headers.Get("retry-after"); len(got) != 1 || got[0] != "2" {
		t.Fatalf("retry-after header: %v", got)
	}
}
