/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package remote

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// fakeHubServer implements just the hub RPCs of ChatCLIService so the client
// wrappers in hub_client.go can be exercised over a real gRPC connection.
type fakeHubServer struct {
	pb.UnimplementedChatCLIServiceServer
	seq      int64
	appended []*pb.AppendEventRequest
	bound    []*pb.SetBindingRequest
}

func (s *fakeHubServer) ResolveActiveConversation(_ context.Context, req *pb.ResolveActiveConversationRequest) (*pb.ResolveActiveConversationResponse, error) {
	p := req.GetPrincipal()
	if p == "" {
		p = "alice"
	}
	return &pb.ResolveActiveConversationResponse{ConvId: "conv-1", Principal: p}, nil
}

func (s *fakeHubServer) NewConversation(_ context.Context, _ *pb.NewConversationRequest) (*pb.NewConversationResponse, error) {
	return &pb.NewConversationResponse{ConvId: "conv-2"}, nil
}

func (s *fakeHubServer) AppendEvent(_ context.Context, req *pb.AppendEventRequest) (*pb.AppendEventResponse, error) {
	s.seq++
	s.appended = append(s.appended, req)
	return &pb.AppendEventResponse{Event: &pb.ConversationEvent{
		ConvId: req.GetConvId(), Seq: s.seq, Channel: req.GetChannel(),
		Role: req.GetRole(), Content: req.GetContent(), Ts: time.Now().UnixMilli(),
	}}, nil
}

func (s *fakeHubServer) ReadConversation(_ context.Context, req *pb.ReadConversationRequest) (*pb.ReadConversationResponse, error) {
	return &pb.ReadConversationResponse{Events: []*pb.ConversationEvent{
		{ConvId: req.GetConvId(), Seq: 1, Channel: "telegram", Role: "user", Content: "hi", Ts: time.Now().UnixMilli()},
	}}, nil
}

func (s *fakeHubServer) SubscribeConversation(req *pb.SubscribeConversationRequest, stream pb.ChatCLIService_SubscribeConversationServer) error {
	return stream.Send(&pb.ConversationEvent{
		ConvId: req.GetConvId(), Seq: 2, Channel: "slack", Role: "assistant", Content: "live", Ts: time.Now().UnixMilli(),
	})
}

func (s *fakeHubServer) SetBinding(_ context.Context, req *pb.SetBindingRequest) (*pb.SetBindingResponse, error) {
	s.bound = append(s.bound, req)
	return &pb.SetBindingResponse{Success: true}, nil
}

func (s *fakeHubServer) ListBindings(_ context.Context, _ *pb.ListBindingsRequest) (*pb.ListBindingsResponse, error) {
	return &pb.ListBindingsResponse{Bindings: []*pb.HubBinding{
		{Platform: "telegram", UserId: "123", Principal: "alice"},
	}}, nil
}

func newHubTestClient(t *testing.T) (*Client, *fakeHubServer) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	fake := &fakeHubServer{}
	srv := grpc.NewServer()
	pb.RegisterChatCLIServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return &Client{conn: conn, grpcClient: pb.NewChatCLIServiceClient(conn), logger: zap.NewNop()}, fake
}

func TestHubClientResolveAppendRead(t *testing.T) {
	c, fake := newHubTestClient(t)
	ctx := context.Background()

	conv, principal, err := c.ResolveActiveConversation(ctx, "")
	if err != nil || conv != "conv-1" || principal != "alice" {
		t.Fatalf("ResolveActiveConversation = %q,%q,%v", conv, principal, err)
	}

	ev, err := c.AppendEvent(ctx, models.ConversationEvent{ConvID: conv, Channel: "local", Role: "user", Content: "hello"})
	if err != nil || ev.Seq == 0 {
		t.Fatalf("AppendEvent = %+v,%v", ev, err)
	}
	if len(fake.appended) != 1 || fake.appended[0].Content != "hello" {
		t.Fatalf("server did not receive append: %+v", fake.appended)
	}

	events, err := c.ReadConversation(ctx, conv, 0, 0)
	if err != nil || len(events) != 1 || events[0].Content != "hi" {
		t.Fatalf("ReadConversation = %+v,%v", events, err)
	}
}

func TestHubClientNewConversationAndBindings(t *testing.T) {
	c, fake := newHubTestClient(t)
	ctx := context.Background()

	conv, err := c.NewConversation(ctx, "")
	if err != nil || conv != "conv-2" {
		t.Fatalf("NewConversation = %q,%v", conv, err)
	}

	if err := c.SetBinding(ctx, "telegram", "123", "alice"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	if len(fake.bound) != 1 || fake.bound[0].UserId != "123" {
		t.Fatalf("server did not receive binding: %+v", fake.bound)
	}

	bindings, err := c.ListBindings(ctx, "")
	if err != nil || len(bindings) != 1 || bindings[0].Principal != "alice" {
		t.Fatalf("ListBindings = %+v,%v", bindings, err)
	}
}

func TestHubClientSubscribe(t *testing.T) {
	c, _ := newHubTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	stream, err := c.SubscribeConversation(ctx, "conv-1", 0)
	if err != nil {
		t.Fatalf("SubscribeConversation: %v", err)
	}
	select {
	case ev, ok := <-stream:
		if !ok {
			t.Fatal("stream closed without an event")
		}
		if ev.Content != "live" || ev.Channel != "slack" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for streamed event")
	}
}
