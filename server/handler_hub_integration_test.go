/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// ctxStream overrides a server stream's context so the test interceptor can
// inject an authenticated UserInfo (mirroring the real auth interceptor).
type ctxStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s ctxStream) Context() context.Context { return s.ctx }

// TestHubGRPCEndToEndTail proves the core promise over the wire: an event
// appended by one client is delivered live to another client tailing the same
// conversation — exactly the Telegram→notebook continuity scenario.
func TestHubGRPCEndToEndTail(t *testing.T) {
	store, err := hub.OpenSQLiteStore(filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	h := &Handler{logger: zap.NewNop()}
	h.SetHub(hub.NewManager(store, nil, 16))

	inject := func(ctx context.Context) context.Context {
		return ContextWithUser(ctx, &UserInfo{Subject: "alice", Role: RoleUser})
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(inject(ctx), req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, ctxStream{ServerStream: ss, ctx: inject(ss.Context())})
		}),
	)
	pb.RegisterChatCLIServiceServer(srv, h)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dial := func() pb.ChatCLIServiceClient {
		conn, err := grpc.NewClient(
			"passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return pb.NewChatCLIServiceClient(conn)
	}

	notebook := dial()
	telegram := dial()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Notebook resolves the shared conversation and starts tailing.
	res, err := notebook.ResolveActiveConversation(ctx, &pb.ResolveActiveConversationRequest{})
	if err != nil {
		t.Fatalf("ResolveActiveConversation: %v", err)
	}
	stream, err := notebook.SubscribeConversation(ctx, &pb.SubscribeConversationRequest{ConvId: res.ConvId})
	if err != nil {
		t.Fatalf("SubscribeConversation: %v", err)
	}

	// A message arrives "via Telegram" on the same conversation.
	if _, err := telegram.AppendEvent(ctx, &pb.AppendEventRequest{
		ConvId:  res.ConvId,
		Channel: "telegram",
		Role:    "user",
		Content: "olá do telegram",
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	got, err := stream.Recv()
	if err != nil {
		t.Fatalf("stream.Recv: %v", err)
	}
	if got.Content != "olá do telegram" || got.Channel != "telegram" {
		t.Fatalf("notebook did not receive the telegram message: %+v", got)
	}
}
