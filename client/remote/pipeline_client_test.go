/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package remote

import (
	"context"
	"errors"
	"testing"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakePipelineServer struct {
	fakePromptServer
	lastChat *pb.ChatTurnRequest
	lastTask *pb.PipelineTaskRequest
	noFinal  bool
	toolErr  bool
}

func (s *fakePipelineServer) ChatTurn(_ context.Context, req *pb.ChatTurnRequest) (*pb.ChatTurnResponse, error) {
	s.lastChat = req
	return &pb.ChatTurnResponse{Reply: "reply to " + req.Text, Session: "alice/" + req.Session}, nil
}
func (s *fakePipelineServer) RunCoder(req *pb.PipelineTaskRequest, stream pb.ChatCLIService_RunCoderServer) error {
	s.lastTask = req
	for _, l := range []string{"one", "two"} {
		if err := stream.Send(&pb.PipelineTaskEvent{Event: &pb.PipelineTaskEvent_Line{Line: l}}); err != nil {
			return err
		}
	}
	if s.noFinal {
		return nil
	}
	return stream.Send(&pb.PipelineTaskEvent{Event: &pb.PipelineTaskEvent_Final{Final: "final answer"}})
}
func (s *fakePipelineServer) RunAgent(req *pb.PipelineTaskRequest, stream pb.ChatCLIService_RunAgentServer) error {
	return status.Error(codes.PermissionDenied, "admin only")
}
func (s *fakePipelineServer) ListPipelineTools(context.Context, *pb.ListPipelineToolsRequest) (*pb.ListPipelineToolsResponse, error) {
	return &pb.ListPipelineToolsResponse{Tools: []*pb.PipelineTool{{Name: "@git", Description: "git", ReadOnly: true}}}, nil
}
func (s *fakePipelineServer) RunPipelineTool(_ context.Context, req *pb.RunPipelineToolRequest) (*pb.RunPipelineToolResponse, error) {
	if s.toolErr {
		return nil, errors.New("boom")
	}
	return &pb.RunPipelineToolResponse{Output: req.Name + " " + req.Args}, nil
}

func TestRemoteClient_PipelineRPCs(t *testing.T) {
	fake := &fakePipelineServer{}
	c := newPromptTestClient(t, fake)
	ctx := context.Background()

	reply, session, err := c.ChatTurn(ctx, "work", "hello", PipelineOpts{Provider: "CLAUDEAI", Plain: true})
	require.NoError(t, err)
	assert.Equal(t, "reply to hello", reply)
	assert.Equal(t, "alice/work", session)
	assert.Equal(t, "CLAUDEAI", fake.lastChat.Provider)
	assert.True(t, fake.lastChat.Plain)

	var lines []string
	final, err := c.RunCoder(ctx, "work", "fix it", PipelineOpts{Quality: map[string]string{"CHATCLI_QUALITY_ENABLED": "true"}}, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, lines)
	assert.Equal(t, "final answer", final)
	assert.Equal(t, "true", fake.lastTask.Quality["CHATCLI_QUALITY_ENABLED"])

	fake.noFinal = true
	final, err = c.RunCoder(ctx, "work", "fix it", PipelineOpts{}, nil)
	require.NoError(t, err)
	assert.Empty(t, final, "a stream that closes without a final event yields nothing")

	_, err = c.RunAgent(ctx, "work", "go", PipelineOpts{}, nil)
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(errors.Unwrap(err)))

	tools, err := c.ListPipelineTools(ctx)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "@git", tools[0].Name)
	assert.True(t, tools[0].ReadOnly)

	out, err := c.RunPipelineTool(ctx, "@git", "status")
	require.NoError(t, err)
	assert.Equal(t, "@git status", out)
	fake.toolErr = true
	_, err = c.RunPipelineTool(ctx, "@git", "status")
	assert.Error(t, err)
}
