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
	"time"

	"github.com/diillson/chatcli/pkg/jwtkey"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// Every bufconn client shares one peer address. With the limiter keyed by
// address (its position before auth until now) two tenants behind one
// ingress shared one bucket; keyed by the authenticated subject each gets
// its own.
func TestRateLimit_RunsAfterAuthAndKeysByJWTSubject(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "unit-secret")
	t.Setenv("CHATCLI_JWT_PUBLIC_KEY", "")
	t.Setenv("CHATCLI_JWT_ISSUER", "")
	t.Setenv("CHATCLI_JWT_AUDIENCE", "")
	t.Setenv("CHATCLI_RATE_LIMIT_RPS", "0.001")
	t.Setenv("CHATCLI_RATE_LIMIT_BURST", "1")
	t.Setenv("CHATCLI_AUDIT_LOG_PATH", "")

	mgr := &mockLLMManager{}
	mgr.On("GetAvailableProviders").Return([]string{"OPENAI"})
	srv := New(Config{Port: 0, Provider: "OPENAI", Model: "m"}, mgr, nil, zap.NewNop())
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.grpcServer.Serve(lis) }()
	t.Cleanup(srv.grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := pb.NewChatCLIServiceClient(conn)

	call := func(sub string) error {
		tok, err := jwtkey.SignHS256(jwtkey.Claims{Subject: sub, Role: "user", ExpiresAt: time.Now().Add(time.Hour).Unix()}, []byte("unit-secret"))
		require.NoError(t, err)
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+tok)
		_, err = client.GetServerInfo(ctx, &pb.GetServerInfoRequest{})
		return err
	}

	require.NoError(t, call("alice"), "alice's first request")
	require.NoError(t, call("bob"), "bob's first request is not charged to alice's bucket")
	err = call("alice")
	require.Error(t, err, "alice's second request exceeds her own burst of 1")
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))

	// An unauthenticated caller is refused by auth before the limiter runs.
	_, err = client.GetServerInfo(context.Background(), &pb.GetServerInfoRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}
