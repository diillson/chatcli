/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * pipeline.go
 *
 * The pipeline RPCs expose the full ChatCLI turn engine over gRPC: a ChatTurn
 * runs the same enrichment an interactive turn gets (memory, /context
 * attachments, skills, knowledge retrieval, token-aware compaction), and
 * RunCoder / RunAgent drive the real agent loops with the server's tools.
 * SendPrompt stays the lean model proxy for clients that run the pipeline
 * themselves (chatcli connect); these RPCs are for thin clients, the operator
 * and automation that want the server to own the whole turn.
 *
 * The backend is opt-in (cmd/server.go, CHATCLI_SERVER_PIPELINE): it hosts a
 * ChatCLI inside the server process, whose turns are serialized by
 * construction. Callers queue on that serialization bounded by their
 * context. Sessions are namespaced by the authenticated principal so two
 * callers never share a conversation by picking the same id.
 */
package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/i18n"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PipelineRunOpts are the per-call routing and quality overrides.
type PipelineRunOpts struct {
	Provider string
	Model    string
	// Quality maps CHATCLI_QUALITY_* overrides applied for the run only.
	Quality map[string]string
	// Plain requests the bare passthrough chat (no enrichment).
	Plain bool
}

// PipelineTool describes one tool the pipeline exposes.
type PipelineTool struct {
	Name        string
	Description string
	Usage       string
	Schema      string
	ReadOnly    bool
}

// PipelineBackend is the slice of the ChatCLI-backed RPC backend the server
// drives. Implemented in cmd/server.go over the same backend the MCP and ACP
// servers use, so every headless surface runs one engine.
type PipelineBackend interface {
	// ChatTurn runs one full-pipeline chat turn on the session.
	ChatTurn(ctx context.Context, session, text string, opts PipelineRunOpts) (string, error)
	// RunCoder runs the coder loop; emit receives rendered transcript lines
	// as the loop works and the returned string is the final answer.
	RunCoder(ctx context.Context, session, task string, opts PipelineRunOpts, emit func(string)) (string, error)
	// RunAgent runs the agent (ReAct) loop with the same contract.
	RunAgent(ctx context.Context, session, task string, opts PipelineRunOpts, emit func(string)) (string, error)
	// Tools lists every tool the exposure policy admits.
	Tools() []PipelineTool
	// CallTool invokes one tool by name with the raw argument string.
	CallTool(ctx context.Context, name, args string) (string, error)
}

// SetPipelineBackend installs the backend; nil keeps the RPCs unavailable.
func (h *Handler) SetPipelineBackend(b PipelineBackend) { h.pipeline = b }

// SetPipelineBackend configures the pipeline backend on the server's handler.
func (s *Server) SetPipelineBackend(b PipelineBackend) { s.handler.SetPipelineBackend(b) }

// pipelineSessionMaxLen bounds caller-chosen session ids.
const pipelineSessionMaxLen = 128

// pipelineOrUnavailable returns the backend or the gRPC error callers get
// when the server was started without one.
func (h *Handler) pipelineOrUnavailable() (PipelineBackend, error) {
	if h.pipeline == nil {
		return nil, status.Errorf(codes.Unavailable, "%s", i18n.T("server.pipeline.disabled"))
	}
	return h.pipeline, nil
}

// pipelineSession namespaces the caller's session by its principal: the
// same id from two users is two conversations. An empty id maps to the
// principal's default session.
func pipelineSession(ctx context.Context, requested string) string {
	principal := "anonymous"
	if u := UserFromContext(ctx); u != nil && u.Subject != "" {
		principal = u.Subject
	}
	sid := strings.TrimSpace(requested)
	if sid == "" {
		sid = "default"
	}
	if len(sid) > pipelineSessionMaxLen {
		sid = sid[:pipelineSessionMaxLen]
	}
	return principal + "/" + sid
}

// requirePipelineExec gates the RPCs that execute on the server host
// (coder, agent, tools): admin only. A chat turn only talks to the model.
func requirePipelineExec(ctx context.Context) error {
	if _, err := RequireRole(ctx, RoleAdmin); err != nil {
		return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.pipeline.exec_requires_admin"))
	}
	return nil
}

// ChatTurn runs one full-pipeline chat turn.
func (h *Handler) ChatTurn(ctx context.Context, req *pb.ChatTurnRequest) (*pb.ChatTurnResponse, error) {
	backend, err := h.pipelineOrUnavailable()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetText()) == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s", i18n.T("server.pipeline.text_required"))
	}
	session := pipelineSession(ctx, req.GetSession())
	reply, err := backend.ChatTurn(ctx, session, req.GetText(), PipelineRunOpts{
		Provider: req.GetProvider(), Model: req.GetModel(), Plain: req.GetPlain(),
	})
	if err != nil {
		h.logger.Error(i18n.T("server.pipeline.chat_failed"), zap.String("session", session), zap.Error(err))
		return nil, status.Errorf(codes.Internal, "%s", i18n.T("server.pipeline.turn_error", err))
	}
	return &pb.ChatTurnResponse{Reply: reply, Session: session}, nil
}

// RunCoder streams a coder run.
func (h *Handler) RunCoder(req *pb.PipelineTaskRequest, stream pb.ChatCLIService_RunCoderServer) error {
	return h.runPipelineTask(req, stream, "coder")
}

// RunAgent streams an agent run.
func (h *Handler) RunAgent(req *pb.PipelineTaskRequest, stream pb.ChatCLIService_RunAgentServer) error {
	return h.runPipelineTask(req, stream, "agent")
}

// taskStream is what RunCoder and RunAgent share of their server streams.
type taskStream interface {
	Context() context.Context
	Send(*pb.PipelineTaskEvent) error
}

// runPipelineTask is the body of RunCoder and RunAgent: transcript lines as
// events while the loop works, the final answer as the last event.
func (h *Handler) runPipelineTask(req *pb.PipelineTaskRequest, stream taskStream, kind string) error {
	ctx := stream.Context()
	backend, err := h.pipelineOrUnavailable()
	if err != nil {
		return err
	}
	if err := requirePipelineExec(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(req.GetTask()) == "" {
		return status.Errorf(codes.InvalidArgument, "%s", i18n.T("server.pipeline.task_required"))
	}
	session := pipelineSession(ctx, req.GetSession())
	opts := PipelineRunOpts{Provider: req.GetProvider(), Model: req.GetModel(), Quality: req.GetQuality()}

	// A Send failure (client went away) is remembered and ends the run
	// through the context the loop observes; emit itself never blocks the
	// loop on a dead stream.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var sendErr error
	emit := func(line string) {
		if sendErr != nil {
			return
		}
		if err := stream.Send(&pb.PipelineTaskEvent{Event: &pb.PipelineTaskEvent_Line{Line: line}}); err != nil {
			sendErr = err
			cancel()
		}
	}

	var (
		final string
		rerr  error
	)
	switch kind {
	case "coder":
		final, rerr = backend.RunCoder(runCtx, session, req.GetTask(), opts, emit)
	default:
		final, rerr = backend.RunAgent(runCtx, session, req.GetTask(), opts, emit)
	}
	if sendErr != nil {
		return sendErr
	}
	if rerr != nil {
		h.logger.Error(i18n.T("server.pipeline.run_failed"), zap.String("kind", kind), zap.String("session", session), zap.Error(rerr))
		return status.Errorf(codes.Internal, "%s", i18n.T("server.pipeline.turn_error", rerr))
	}
	return stream.Send(&pb.PipelineTaskEvent{Event: &pb.PipelineTaskEvent_Final{Final: final}})
}

// ListPipelineTools lists the tools the pipeline exposes.
func (h *Handler) ListPipelineTools(ctx context.Context, _ *pb.ListPipelineToolsRequest) (*pb.ListPipelineToolsResponse, error) {
	backend, err := h.pipelineOrUnavailable()
	if err != nil {
		return nil, err
	}
	if err := requirePipelineExec(ctx); err != nil {
		return nil, err
	}
	tools := backend.Tools()
	resp := &pb.ListPipelineToolsResponse{Tools: make([]*pb.PipelineTool, 0, len(tools))}
	for _, t := range tools {
		resp.Tools = append(resp.Tools, &pb.PipelineTool{
			Name: t.Name, Description: t.Description, Usage: t.Usage, Schema: t.Schema, ReadOnly: t.ReadOnly,
		})
	}
	return resp, nil
}

// RunPipelineTool invokes one tool.
func (h *Handler) RunPipelineTool(ctx context.Context, req *pb.RunPipelineToolRequest) (*pb.RunPipelineToolResponse, error) {
	backend, err := h.pipelineOrUnavailable()
	if err != nil {
		return nil, err
	}
	if err := requirePipelineExec(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s", i18n.T("server.pipeline.tool_required"))
	}
	out, err := backend.CallTool(ctx, name, req.GetArgs())
	if err != nil {
		h.logger.Warn(i18n.T("server.pipeline.tool_failed"), zap.String("tool", name), zap.Error(err))
		return nil, status.Errorf(codes.Internal, "%s", fmt.Sprintf("%s: %v", name, err))
	}
	return &pb.RunPipelineToolResponse{Output: out}, nil
}
