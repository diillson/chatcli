/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/fallback"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// buildRecorder is a routeManager that records every client it builds.
type buildRecorder struct {
	routeManager
	mu    sync.Mutex
	built []string
}

func (b *buildRecorder) GetClient(provider, model string) (client.LLMClient, error) {
	b.mu.Lock()
	b.built = append(b.built, provider)
	b.mu.Unlock()
	return b.routeManager.GetClient(provider, model)
}

func (b *buildRecorder) CreateClientWithKey(provider, model, _ string) (client.LLMClient, error) {
	return b.GetClient(provider, model)
}

func (b *buildRecorder) builtAgent() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, p := range b.built {
		if isAgentProvider(p) {
			return true
		}
	}
	return false
}

func agentGuardHandler(defaultProvider string) (*Handler, *buildRecorder, *routeClient) {
	devin := &routeClient{model: "devin-model", replies: []string{"ran on host", "ran on host", "ran on host"}}
	mgr := &buildRecorder{routeManager: routeManager{byProvider: map[string]client.LLMClient{
		"OPENAI": &routeClient{model: "gpt-6-astra", replies: []string{"ok", "ok", "ok"}},
		"DEVIN":  devin,
	}}}
	return NewHandler(mgr, nil, zap.NewNop(), defaultProvider, ""), mgr, devin
}

// sessionStream is a scripted InteractiveSession stream.
type sessionStream struct {
	grpc.ServerStream
	ctx  context.Context
	in   []*pb.SessionMessage
	sent []*pb.SessionMessage
}

func (s *sessionStream) Context() context.Context { return s.ctx }
func (s *sessionStream) Send(m *pb.SessionMessage) error {
	s.sent = append(s.sent, m)
	return nil
}
func (s *sessionStream) Recv() (*pb.SessionMessage, error) {
	if len(s.in) == 0 {
		return nil, io.EOF
	}
	m := s.in[0]
	s.in = s.in[1:]
	return m, nil
}

// rpcPaths runs one turn through every RPC that resolves an LLM route and
// returns each path's error.
func rpcPaths(h *Handler, role UserRole, provider string) map[string]error {
	ctx := principalCtx("caller", role, authSourceJWT)
	out := map[string]error{}
	_, out["SendPrompt"] = h.SendPrompt(ctx, &pb.SendPromptRequest{Prompt: "hi", Provider: provider})
	out["StreamPrompt"] = h.StreamPrompt(&pb.StreamPromptRequest{Prompt: "hi", Provider: provider}, &fakeStreamServer{ctx: ctx})
	out["InteractiveSession"] = h.InteractiveSession(&sessionStream{ctx: ctx, in: []*pb.SessionMessage{{
		Type: pb.SessionMessage_USER_INPUT, Content: "hi", Metadata: map[string]string{"provider": provider},
	}}})
	_, out["AnalyzeIssue"] = h.AnalyzeIssue(ctx, &pb.AnalyzeIssueRequest{IssueName: "i", Provider: provider})
	_, out["AgenticStep"] = h.AgenticStep(ctx, &pb.AgenticStepRequest{IssueName: "i", Provider: provider})
	_, out["ChatTurn"] = h.ChatTurn(ctx, &pb.ChatTurnRequest{Text: "hi", Provider: provider})
	return out
}

func TestAgentProvider_NonAdminSelectingItIsDeniedOnEveryPath(t *testing.T) {
	for _, role := range []UserRole{RoleReadonly, RoleUser} {
		t.Run(string(role), func(t *testing.T) {
			h, mgr, devin := agentGuardHandler("OPENAI")
			h.SetPipelineBackend(&fakePipeline{reply: "pipeline"})
			for path, err := range rpcPaths(h, role, "devin") {
				if status.Code(err) != codes.PermissionDenied {
					t.Errorf("%s: got %v, want PermissionDenied", path, err)
				}
			}
			if mgr.builtAgent() || atomic.LoadInt32(&devin.calls) != 0 {
				t.Fatal("an agent provider client was built or called for a non-admin")
			}
		})
	}
}

func TestAgentProvider_AdminMaySelectIt(t *testing.T) {
	h, _, devin := agentGuardHandler("OPENAI")
	ctx := principalCtx("root", RoleAdmin, authSourceLegacy)
	resp, err := h.SendPrompt(ctx, &pb.SendPromptRequest{Prompt: "hi", Provider: "DEVIN"})
	if err != nil {
		t.Fatalf("admin DEVIN turn: %v", err)
	}
	if resp.Response != "ran on host" || atomic.LoadInt32(&devin.calls) != 1 {
		t.Fatalf("admin turn did not reach DEVIN: %+v calls=%d", resp, atomic.LoadInt32(&devin.calls))
	}
	if _, err := h.ChatTurn(ctx, &pb.ChatTurnRequest{Text: "hi", Provider: "DEVIN"}); status.Code(err) == codes.PermissionDenied {
		t.Fatalf("admin ChatTurn denied: %v", err)
	}
}

func TestAgentProvider_DefaultAgentServerRefusesNonAdmins(t *testing.T) {
	h, mgr, _ := agentGuardHandler("DEVIN")
	h.SetPipelineBackend(&fakePipeline{reply: "pipeline"})
	ctx := principalCtx("caller", RoleUser, authSourceJWT)
	for path, err := range rpcPaths(h, RoleUser, "") {
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s: got %v, want PermissionDenied", path, err)
			continue
		}
		if s := status.Convert(err).Message(); !strings.Contains(s, "LLM_PROVIDER") {
			t.Errorf("%s: refusal does not name the setting: %q", path, s)
		}
	}
	if mgr.builtAgent() {
		t.Fatal("the default agent provider was built for a non-admin")
	}

	// Naming a model provider explicitly still works for the same caller.
	if _, err := h.SendPrompt(ctx, &pb.SendPromptRequest{Prompt: "hi", Provider: "OPENAI"}); err != nil {
		t.Fatalf("explicit model provider: %v", err)
	}
	// And the admin keeps the default.
	if _, err := h.SendPrompt(principalCtx("root", RoleAdmin, authSourceLegacy), &pb.SendPromptRequest{Prompt: "hi"}); err != nil {
		t.Fatalf("admin on the default agent provider: %v", err)
	}
}

func TestAgentProvider_ChainWithAgentRefusesNonAdmins(t *testing.T) {
	h, _, _ := agentGuardHandler("OPENAI")
	chained := &routeClient{model: "devin-model", replies: []string{"ran on host"}}
	h.SetFallbackChain(fallback.NewChain(zap.NewNop(), []fallback.FallbackEntry{
		{Provider: "OPENAI", Model: "gpt-6-astra", Client: &routeClient{model: "gpt-6-astra", errs: []error{io.ErrUnexpectedEOF}}, Priority: 0},
		{Provider: "DEVIN", Model: "devin-model", Client: chained, Priority: 1},
	}, fallback.WithMaxRetries(0)))

	_, err := h.SendPrompt(principalCtx("caller", RoleUser, authSourceJWT), &pb.SendPromptRequest{Prompt: "hi"})
	if status.Code(err) != codes.PermissionDenied || !strings.Contains(status.Convert(err).Message(), "CHATCLI_FALLBACK_PROVIDERS") {
		t.Fatalf("chain with an agent: got %v, want PermissionDenied naming CHATCLI_FALLBACK_PROVIDERS", err)
	}
	if atomic.LoadInt32(&chained.calls) != 0 {
		t.Fatal("the chain reached the agent provider for a non-admin")
	}
}
