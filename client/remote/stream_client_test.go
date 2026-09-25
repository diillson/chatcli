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

	llmclient "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// fakePromptServer answers SendPrompt and StreamPrompt the way the real
// server does after the request-path parity work: attribution, usage and
// stop reason on the reply, text chunks then a done message on the stream.
type fakePromptServer struct {
	pb.UnimplementedChatCLIServiceServer
	lastSend   *pb.SendPromptRequest
	lastStream *pb.StreamPromptRequest
	legacyDone bool // close the stream without a done message (older servers)
}

func (s *fakePromptServer) SendPrompt(_ context.Context, req *pb.SendPromptRequest) (*pb.SendPromptResponse, error) {
	s.lastSend = req
	return &pb.SendPromptResponse{
		Response: "answer", Model: "model-b", Provider: "PB",
		Usage:      &pb.TokenUsage{PromptTokens: 11, CompletionTokens: 3, CacheReadTokens: 5},
		StopReason: "end_turn",
	}, nil
}

func (s *fakePromptServer) StreamPrompt(req *pb.StreamPromptRequest, stream pb.ChatCLIService_StreamPromptServer) error {
	s.lastStream = req
	for _, c := range []string{"str", "eam"} {
		if err := stream.Send(&pb.StreamPromptResponse{Chunk: c, Model: "m", Provider: "P"}); err != nil {
			return err
		}
	}
	if s.legacyDone {
		return nil
	}
	return stream.Send(&pb.StreamPromptResponse{Done: true, Model: "model-c", Provider: "PC",
		Usage: &pb.TokenUsage{PromptTokens: 4, CompletionTokens: 2, Estimated: true}, StopReason: "max_tokens"})
}

func newPromptTestClient(t *testing.T, fake pb.ChatCLIServiceServer) *Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterChatCLIServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return &Client{conn: conn, grpcClient: pb.NewChatCLIServiceClient(conn), logger: zap.NewNop(), model: "default", provider: "DEFAULT"}
}

func TestRemoteClient_SendPromptRecordsUsageAndAttribution(t *testing.T) {
	fake := &fakePromptServer{}
	c := newPromptTestClient(t, fake)
	assert.Nil(t, c.LastUsage(), "nothing sent yet")

	reply, err := c.SendPrompt(context.Background(), "hi", []models.Message{{Role: "user", Content: "earlier"}}, 0)
	require.NoError(t, err)
	assert.Equal(t, "answer", reply)
	require.Len(t, fake.lastSend.History, 1)
	assert.Equal(t, "earlier", fake.lastSend.History[0].Content)

	usage := c.LastUsage()
	require.NotNil(t, usage)
	assert.Equal(t, 11, usage.PromptTokens)
	assert.Equal(t, 5, usage.CacheReadInputTokens)
	assert.Equal(t, 14, usage.TotalTokens)
	assert.True(t, usage.IsReal)
	assert.Equal(t, "end_turn", c.LastStopReason())
	assert.Equal(t, "model-b", c.GetModelName(), "attribution follows the server")
	assert.Equal(t, "PB", c.GetProvider())

	var _ llmclient.UsageAwareClient = c
	var _ llmclient.StopReasonAwareClient = c
	var _ llmclient.StreamingClient = c
}

func TestRemoteClient_PinnedOverridesAreNotRewrittenByAttribution(t *testing.T) {
	c := newPromptTestClient(t, &fakePromptServer{})
	c.overModel, c.overProvider = "pinned", "PINNED"
	c.model, c.provider = "pinned", "PINNED"
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "pinned", c.GetModelName())
	assert.Equal(t, "PINNED", c.GetProvider())
}

func TestRemoteClient_StreamsChunksAndFinalUsage(t *testing.T) {
	fake := &fakePromptServer{}
	c := newPromptTestClient(t, fake)
	assert.True(t, c.SupportsStreaming())
	sc, ok := llmclient.AsStreamingClient(c)
	require.True(t, ok)

	ch, err := sc.SendPromptStream(context.Background(), "hello", nil, 77)
	require.NoError(t, err)
	text, usage, stop, err := llmclient.DrainStream(ch)
	require.NoError(t, err)
	assert.Equal(t, "stream", text)
	require.NotNil(t, usage)
	assert.Equal(t, 4, usage.PromptTokens)
	assert.False(t, usage.IsReal, "the server said it estimated")
	assert.Equal(t, "max_tokens", stop)
	assert.Equal(t, int32(77), fake.lastStream.MaxTokens)
	assert.Equal(t, "model-c", c.GetModelName())
	assert.Equal(t, "PC", c.GetProvider())
	assert.Equal(t, "max_tokens", c.LastStopReason())
}

func TestRemoteClient_StreamFromOlderServerWithoutDoneMessage(t *testing.T) {
	c := newPromptTestClient(t, &fakePromptServer{legacyDone: true})
	ch, err := c.SendPromptStream(context.Background(), "hello", nil, 0)
	require.NoError(t, err)
	text, usage, _, err := llmclient.DrainStream(ch)
	require.NoError(t, err)
	assert.Equal(t, "stream", text)
	assert.Nil(t, usage)
}

func TestUsageFromProto(t *testing.T) {
	assert.Nil(t, usageFromProto(nil))
	u := usageFromProto(&pb.TokenUsage{PromptTokens: 1, CompletionTokens: 2, CacheWriteTokens: 3, ReasoningTokens: 4})
	assert.Equal(t, 3, u.TotalTokens)
	assert.Equal(t, 3, u.CacheCreationInputTokens)
	assert.Equal(t, 4, u.ReasoningTokens)
	assert.True(t, u.IsReal)
}
