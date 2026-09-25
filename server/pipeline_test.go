/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakePipeline records calls and scripts replies.
type fakePipeline struct {
	sessions []string
	lines    []string
	reply    string
	err      error
	tools    []PipelineTool
	toolOut  string
	lastOpts PipelineRunOpts
}

func (f *fakePipeline) ChatTurn(_ context.Context, session, text string, opts PipelineRunOpts) (string, error) {
	f.sessions = append(f.sessions, session)
	f.lastOpts = opts
	return f.reply + ":" + text, f.err
}
func (f *fakePipeline) RunCoder(ctx context.Context, session, task string, opts PipelineRunOpts, emit func(string)) (string, error) {
	f.sessions = append(f.sessions, session)
	f.lastOpts = opts
	for _, l := range f.lines {
		emit(l)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return f.reply + ":" + task, f.err
}
func (f *fakePipeline) RunAgent(ctx context.Context, session, task string, opts PipelineRunOpts, emit func(string)) (string, error) {
	return f.RunCoder(ctx, session, task, opts, emit)
}
func (f *fakePipeline) Tools() []PipelineTool { return f.tools }
func (f *fakePipeline) CallTool(_ context.Context, name, args string) (string, error) {
	return f.toolOut + ":" + name + ":" + args, f.err
}

func pipelineHandler(fp *fakePipeline) *Handler {
	mgr := &mockLLMManager{}
	mgr.On("GetAvailableProviders").Return([]string{"OPENAI"})
	h := NewHandler(mgr, nil, zap.NewNop(), "OPENAI", "gpt-6-astra")
	if fp != nil {
		h.SetPipelineBackend(fp)
	}
	return h
}

func asUser(ctx context.Context, subject string, role UserRole) context.Context {
	return ContextWithUser(ctx, &UserInfo{Subject: subject, Role: role})
}

// fakeTaskStream captures task events.
type fakeTaskStream struct {
	grpc.ServerStream
	ctx     context.Context
	events  []*pb.PipelineTaskEvent
	failAt  int // fail the n-th Send (1-based); 0 never
	sent    int
	sendErr error
}

func (f *fakeTaskStream) Context() context.Context { return f.ctx }
func (f *fakeTaskStream) Send(ev *pb.PipelineTaskEvent) error {
	f.sent++
	if f.failAt > 0 && f.sent == f.failAt {
		f.sendErr = errors.New("client went away")
		return f.sendErr
	}
	f.events = append(f.events, ev)
	return nil
}
func (f *fakeTaskStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeTaskStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeTaskStream) SetTrailer(metadata.MD)       {}

func TestPipeline_UnavailableWithoutBackend(t *testing.T) {
	h := pipelineHandler(nil)
	ctx := asUser(context.Background(), "alice", RoleAdmin)
	_, err := h.ChatTurn(ctx, &pb.ChatTurnRequest{Text: "hi"})
	assert.Equal(t, codes.Unavailable, status.Code(err))
	_, err = h.ListPipelineTools(ctx, &pb.ListPipelineToolsRequest{})
	assert.Equal(t, codes.Unavailable, status.Code(err))
	_, err = h.RunPipelineTool(ctx, &pb.RunPipelineToolRequest{Name: "x"})
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(t, codes.Unavailable, status.Code(h.RunCoder(&pb.PipelineTaskRequest{Task: "t"}, &fakeTaskStream{ctx: ctx})))

	info, err := h.GetServerInfo(ctx, &pb.GetServerInfoRequest{})
	require.NoError(t, err)
	assert.False(t, info.PipelineEnabled)
}

func TestChatTurn_NamespacesSessionsByPrincipal(t *testing.T) {
	fp := &fakePipeline{reply: "ok"}
	h := pipelineHandler(fp)

	resp, err := h.ChatTurn(asUser(context.Background(), "alice", RoleUser), &pb.ChatTurnRequest{Session: "work", Text: "hello", Provider: "CLAUDEAI", Plain: true})
	require.NoError(t, err)
	assert.Equal(t, "ok:hello", resp.Reply)
	assert.Equal(t, "alice/work", resp.Session)
	assert.Equal(t, "CLAUDEAI", fp.lastOpts.Provider)
	assert.True(t, fp.lastOpts.Plain)

	_, err = h.ChatTurn(asUser(context.Background(), "bob", RoleReadonly), &pb.ChatTurnRequest{Session: "work", Text: "hello"})
	require.NoError(t, err, "a chat turn needs no admin role")
	assert.Equal(t, []string{"alice/work", "bob/work"}, fp.sessions, "same id, two conversations")

	resp, err = h.ChatTurn(context.Background(), &pb.ChatTurnRequest{Text: "x"})
	require.NoError(t, err)
	assert.Equal(t, "anonymous/default", resp.Session, "no principal, no session id")

	long := strings.Repeat("s", pipelineSessionMaxLen+10)
	resp, _ = h.ChatTurn(asUser(context.Background(), "alice", RoleUser), &pb.ChatTurnRequest{Session: long, Text: "x"})
	assert.Len(t, resp.Session, len("alice/")+pipelineSessionMaxLen)

	_, err = h.ChatTurn(asUser(context.Background(), "alice", RoleUser), &pb.ChatTurnRequest{Text: "   "})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	fp.err = errors.New("provider down")
	_, err = h.ChatTurn(asUser(context.Background(), "alice", RoleUser), &pb.ChatTurnRequest{Text: "x"})
	assert.Equal(t, codes.Internal, status.Code(err))

	info, err := h.GetServerInfo(context.Background(), &pb.GetServerInfoRequest{})
	require.NoError(t, err)
	assert.True(t, info.PipelineEnabled)
}

func TestRunCoder_RequiresAdminStreamsLinesThenFinal(t *testing.T) {
	fp := &fakePipeline{reply: "done", lines: []string{"step 1", "step 2"}}
	h := pipelineHandler(fp)

	err := h.RunCoder(&pb.PipelineTaskRequest{Task: "fix"}, &fakeTaskStream{ctx: asUser(context.Background(), "alice", RoleUser)})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "coder runs execute on the host")

	st := &fakeTaskStream{ctx: asUser(context.Background(), "alice", RoleAdmin)}
	require.NoError(t, h.RunCoder(&pb.PipelineTaskRequest{Session: "s", Task: "fix", Quality: map[string]string{"CHATCLI_QUALITY_ENABLED": "true"}}, st))
	require.Len(t, st.events, 3)
	assert.Equal(t, "step 1", st.events[0].GetLine())
	assert.Equal(t, "step 2", st.events[1].GetLine())
	assert.Equal(t, "done:fix", st.events[2].GetFinal())
	assert.Equal(t, "alice/s", fp.sessions[0])
	assert.Equal(t, "true", fp.lastOpts.Quality["CHATCLI_QUALITY_ENABLED"])

	st = &fakeTaskStream{ctx: asUser(context.Background(), "alice", RoleAdmin)}
	err = h.RunAgent(&pb.PipelineTaskRequest{Task: ""}, st)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	// The client goes away mid-run: the send error ends the run.
	st = &fakeTaskStream{ctx: asUser(context.Background(), "alice", RoleAdmin), failAt: 1}
	err = h.RunAgent(&pb.PipelineTaskRequest{Task: "go"}, st)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client went away")

	fp.err = errors.New("loop crashed")
	st = &fakeTaskStream{ctx: asUser(context.Background(), "alice", RoleAdmin)}
	err = h.RunCoder(&pb.PipelineTaskRequest{Task: "go"}, st)
	assert.Equal(t, codes.Internal, status.Code(err))
}

func TestPipelineTools_ListAndRunRequireAdmin(t *testing.T) {
	fp := &fakePipeline{tools: []PipelineTool{{Name: "@git", Description: "git", Usage: "@git status", ReadOnly: true}}, toolOut: "out"}
	h := pipelineHandler(fp)
	user := asUser(context.Background(), "alice", RoleUser)
	admin := asUser(context.Background(), "alice", RoleAdmin)

	_, err := h.ListPipelineTools(user, &pb.ListPipelineToolsRequest{})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	resp, err := h.ListPipelineTools(admin, &pb.ListPipelineToolsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Tools, 1)
	assert.Equal(t, "@git", resp.Tools[0].Name)
	assert.True(t, resp.Tools[0].ReadOnly)

	_, err = h.RunPipelineTool(user, &pb.RunPipelineToolRequest{Name: "@git", Args: "status"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	out, err := h.RunPipelineTool(admin, &pb.RunPipelineToolRequest{Name: " @git ", Args: "status"})
	require.NoError(t, err)
	assert.Equal(t, "out:@git:status", out.Output)
	_, err = h.RunPipelineTool(admin, &pb.RunPipelineToolRequest{Name: " "})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	fp.err = errors.New("tool exploded")
	_, err = h.RunPipelineTool(admin, &pb.RunPipelineToolRequest{Name: "@git"})
	assert.Equal(t, codes.Internal, status.Code(err))
}

func TestPipelineValidators(t *testing.T) {
	assert.NoError(t, validateChatTurn(&pb.ChatTurnRequest{Session: "s", Text: "hi"}))
	assert.Error(t, validateChatTurn(&pb.ChatTurnRequest{Text: strings.Repeat("x", maxPromptBytes+1)}))
	assert.Error(t, validateChatTurn(&pb.ChatTurnRequest{Session: strings.Repeat("s", pipelineSessionMaxLen+1), Text: "x"}))
	assert.NoError(t, validateChatTurn("not a request"), "foreign types pass through")

	assert.NoError(t, validatePipelineTask(&pb.PipelineTaskRequest{Task: "t", Quality: map[string]string{"CHATCLI_QUALITY_ENABLED": "true"}}))
	big := map[string]string{}
	for i := 0; i < maxPipelineQualityKeys+1; i++ {
		big[strings.Repeat("k", i+1)] = "v"
	}
	assert.Error(t, validatePipelineTask(&pb.PipelineTaskRequest{Task: "t", Quality: big}))
	assert.Error(t, validatePipelineTask(&pb.PipelineTaskRequest{Task: "t", Quality: map[string]string{"k": strings.Repeat("v", maxPipelineQualityValue+1)}}))
	assert.Error(t, validatePipelineTask(&pb.PipelineTaskRequest{Task: "t", Quality: map[string]string{strings.Repeat("k", maxSessionNameLen+1): "v"}}))

	assert.NoError(t, validateRunPipelineTool(&pb.RunPipelineToolRequest{Name: "@git", Args: "status"}))
	assert.Error(t, validateRunPipelineTool(&pb.RunPipelineToolRequest{Name: strings.Repeat("n", maxPipelineToolNameLen+1)}))
	assert.Error(t, validateRunPipelineTool(&pb.RunPipelineToolRequest{Name: "@git", Args: strings.Repeat("a", maxPromptBytes+1)}))
}
