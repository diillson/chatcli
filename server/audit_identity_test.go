/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"net"
)

const sendPromptMethod = "/chatcli.v1.ChatCLIService/SendPrompt"

// auditFields runs an audited + authenticated unary call and returns the
// fields of the audit line it produced.
func auditFields(t *testing.T, token, presented string) map[string]interface{} {
	t.Helper()

	core, recorded := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	audit := NewAuditLogger(logger)
	auth := NewTokenAuthInterceptor(token, logger)

	ctx := peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP("10.0.1.50"), Port: 54321},
	})
	if presented != "" {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+presented))
	}

	info := &grpc.UnaryServerInfo{FullMethod: sendPromptMethod}
	handler := func(context.Context, interface{}) (interface{}, error) { return &pb.SendPromptResponse{}, nil }

	_, _ = audit.UnaryInterceptor()(ctx, &pb.SendPromptRequest{Prompt: "hi"}, info,
		func(inner context.Context, req interface{}) (interface{}, error) {
			return auth.Unary()(inner, req, info, handler)
		})

	entries := recorded.FilterMessage("audit").All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one audit line, got %d", len(entries))
	}
	return entries[0].ContextMap()
}

// Before the auth outcome was carried outward, every audit line said
// "anonymous" — the audit interceptor wraps auth, so the identity auth
// injects inward was never visible on the way out.
func TestAudit_NamesTheAuthenticatedCaller(t *testing.T) {
	fields := auditFields(t, "s3cret", "s3cret")

	if got := fields["actor"]; got != "user:legacy-token" {
		t.Errorf("actor = %v, want %q", got, "user:legacy-token")
	}
	if got := fields["role"]; got != string(RoleAdmin) {
		t.Errorf("role = %v, want %q", got, RoleAdmin)
	}
	if got := fields["client_id"]; got == "anonymous" {
		t.Errorf("client_id is still %q for an authenticated call", got)
	}
	if got := fields["action"]; got != "SendPrompt" {
		t.Errorf("action = %v, want %q", got, "SendPrompt")
	}
	if got := fields["ip"]; got != "10.0.1.50" {
		t.Errorf("ip = %v, want the caller host without the ephemeral port", got)
	}
	if got := fields["result"]; got != "success" {
		t.Errorf("result = %v, want success", got)
	}
}

// A refused call is a security event and must not read like a handler
// failure.
func TestAudit_RecordsDeniedDistinctlyFromError(t *testing.T) {
	fields := auditFields(t, "s3cret", "wrong-token")

	if got := fields["result"]; got != "denied" {
		t.Errorf("result = %v, want %q", got, "denied")
	}
	if got := fields["actor"]; got != "anonymous" {
		t.Errorf("actor = %v, want anonymous for a refused call", got)
	}
	if got := fields["ip"]; got != "10.0.1.50" {
		t.Errorf("ip = %v, want the caller host even when refused", got)
	}
}

// The streaming half of the trail: StreamPrompt and InteractiveSession
// carry the conversation, and left no audit line at all before.
func TestAudit_StreamInterceptorLeavesATrail(t *testing.T) {
	core, recorded := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	audit := NewAuditLogger(logger)
	auth := NewTokenAuthInterceptor("s3cret", logger)

	ctx := peer.NewContext(context.Background(), &peer.Peer{
		Addr: &net.TCPAddr{IP: net.ParseIP("10.0.1.51"), Port: 4444},
	})
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer s3cret"))

	info := &grpc.StreamServerInfo{FullMethod: "/chatcli.v1.ChatCLIService/StreamPrompt"}
	stream := &fakeServerStream{ctx: ctx}

	err := audit.StreamInterceptor()(nil, stream, info,
		func(srv interface{}, ss grpc.ServerStream) error {
			return auth.Stream()(srv, ss, info, func(interface{}, grpc.ServerStream) error { return nil })
		})
	if err != nil {
		t.Fatalf("stream handler: %v", err)
	}

	entries := recorded.FilterMessage("audit").All()
	if len(entries) != 1 {
		t.Fatalf("expected one audit line for the stream, got %d", len(entries))
	}
	fields := entries[0].ContextMap()
	if got := fields["action"]; got != "StreamPrompt" {
		t.Errorf("action = %v, want StreamPrompt", got)
	}
	if got := fields["actor"]; got != "user:legacy-token" {
		t.Errorf("actor = %v, want the authenticated caller", got)
	}
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }
func (f *fakeServerStream) RecvMsg(interface{}) error {
	return nil
}
func (f *fakeServerStream) SendMsg(interface{}) error { return nil }
