/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"net"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func authCtx(token, host string) context.Context {
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(host), Port: 40000}})
	return metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+token))
}

// The failure limiter must only count failures. It used to spend a token on
// every bearer call, so a client making more than five authenticated calls
// a minute — the operator's probe and pipeline, a busy CLI — was refused
// with valid credentials.
func TestAuthFailureLimiter_NeverThrottlesValidCredentials(t *testing.T) {
	ai := NewTokenAuthInterceptor("valid-token", zap.NewNop())
	for i := 0; i < 50; i++ {
		if _, err := ai.authorize(authCtx("valid-token", "10.0.0.7"), "/chatcli.v1.ChatCLIService/SendPrompt"); err != nil {
			t.Fatalf("call %d with a valid token was refused: %v", i+1, err)
		}
	}
}

func TestAuthFailureLimiter_StillLimitsFailures(t *testing.T) {
	ai := NewTokenAuthInterceptor("valid-token", zap.NewNop())
	for i := 0; i < 5; i++ {
		if _, err := ai.authorize(authCtx("wrong", "10.0.0.8"), "/chatcli.v1.ChatCLIService/SendPrompt"); err == nil {
			t.Fatal("a wrong token was accepted")
		}
	}
	// Five failures spend the burst: the host is now locked out, even with
	// the right token, until the limiter refills.
	if _, err := ai.authorize(authCtx("valid-token", "10.0.0.8"), "/chatcli.v1.ChatCLIService/SendPrompt"); err == nil {
		t.Fatal("a host that just failed five times was not rate limited")
	}
	// Another host is unaffected.
	if _, err := ai.authorize(authCtx("valid-token", "10.0.0.9"), "/chatcli.v1.ChatCLIService/SendPrompt"); err != nil {
		t.Fatalf("an unrelated host was limited: %v", err)
	}
}
