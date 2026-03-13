/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SendPrompt handles a single prompt request.
func (h *Handler) SendPrompt(ctx context.Context, req *pb.SendPromptRequest) (*pb.SendPromptResponse, error) {
	if req.Prompt == "" {
		return nil, status.Error(codes.InvalidArgument, "prompt cannot be empty")
	}

	llmClient, err := h.getClient(req.Provider, req.Model, req.ClientApiKey, req.ProviderConfig)
	if err != nil {
		h.logger.Error("Failed to get LLM client", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get LLM client: %v", err)
	}

	history := protoToHistory(req.History)
	maxTokens := int(req.MaxTokens)

	enrichedPrompt := h.enrichPrompt(req.Prompt)
	response, err := llmClient.SendPrompt(ctx, enrichedPrompt, history, maxTokens)
	if err != nil {
		h.logger.Error("LLM SendPrompt failed", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "LLM error: %v", err)
	}

	provider := req.Provider
	if provider == "" {
		provider = h.defaultProvider
	}

	return &pb.SendPromptResponse{
		Response: response,
		Model:    llmClient.GetModelName(),
		Provider: provider,
	}, nil
}

// StreamPrompt handles a streaming prompt request.
func (h *Handler) StreamPrompt(req *pb.StreamPromptRequest, stream pb.ChatCLIService_StreamPromptServer) error {
	if req.Prompt == "" {
		return status.Error(codes.InvalidArgument, "prompt cannot be empty")
	}

	llmClient, err := h.getClient(req.Provider, req.Model, req.ClientApiKey, req.ProviderConfig)
	if err != nil {
		h.logger.Error("Failed to get LLM client for stream", zap.Error(err))
		return status.Errorf(codes.Internal, "failed to get LLM client: %v", err)
	}

	history := protoToHistory(req.History)
	maxTokens := int(req.MaxTokens)

	enrichedPrompt := h.enrichPrompt(req.Prompt)
	response, err := llmClient.SendPrompt(stream.Context(), enrichedPrompt, history, maxTokens)
	if err != nil {
		h.logger.Error("LLM StreamPrompt failed", zap.Error(err))
		return status.Errorf(codes.Internal, "LLM error: %v", err)
	}

	provider := req.Provider
	if provider == "" {
		provider = h.defaultProvider
	}

	modelName := llmClient.GetModelName()

	chunks := chunkResponse(response, streamChunkSize)
	for i, chunk := range chunks {
		isLast := i == len(chunks)-1
		if err := stream.Send(&pb.StreamPromptResponse{
			Chunk:    chunk,
			Done:     isLast,
			Model:    modelName,
			Provider: provider,
		}); err != nil {
			return err
		}
	}

	return nil
}

// InteractiveSession handles bidirectional streaming for interactive mode.
func (h *Handler) InteractiveSession(stream pb.ChatCLIService_InteractiveSessionServer) error {
	h.logger.Info("Interactive session started")
	if h.sessionMetrics != nil {
		h.sessionMetrics.ActiveSessions.Inc()
		defer h.sessionMetrics.ActiveSessions.Dec()
	}

	var (
		history []models.Message
		mu      sync.Mutex
	)

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			h.logger.Info("Interactive session ended (client closed)")
			return nil
		}
		if err != nil {
			h.logger.Error("Interactive session recv error", zap.Error(err))
			return err
		}

		switch msg.Type {
		case pb.SessionMessage_USER_INPUT:
			mu.Lock()
			history = append(history, models.Message{Role: "user", Content: msg.Content})

			llmClient, err := h.getClient(msg.Metadata["provider"], msg.Metadata["model"], msg.Metadata["client_api_key"], nil)
			if err != nil {
				mu.Unlock()
				sendErr := stream.Send(&pb.SessionMessage{
					Type:    pb.SessionMessage_ERROR,
					Content: fmt.Sprintf("Failed to get LLM client: %v", err),
				})
				if sendErr != nil {
					return sendErr
				}
				continue
			}

			maxTokens := 0 // use default
			enrichedContent := h.enrichPrompt(msg.Content)
			response, err := llmClient.SendPrompt(stream.Context(), enrichedContent, history, maxTokens)
			if err != nil {
				mu.Unlock()
				sendErr := stream.Send(&pb.SessionMessage{
					Type:    pb.SessionMessage_ERROR,
					Content: fmt.Sprintf("LLM error: %v", err),
				})
				if sendErr != nil {
					return sendErr
				}
				continue
			}

			history = append(history, models.Message{Role: "assistant", Content: response})
			mu.Unlock()

			if err := stream.Send(&pb.SessionMessage{
				Type:    pb.SessionMessage_ASSISTANT_RESPONSE,
				Content: response,
				Metadata: map[string]string{
					"model":    llmClient.GetModelName(),
					"provider": h.defaultProvider,
				},
			}); err != nil {
				return err
			}

		case pb.SessionMessage_COMMAND:
			result := h.executeRemoteCommand(msg.Content)
			if err := stream.Send(&pb.SessionMessage{
				Type:    pb.SessionMessage_COMMAND_RESULT,
				Content: result,
			}); err != nil {
				return err
			}

		default:
			h.logger.Warn("Unhandled session message type", zap.Int32("type", int32(msg.Type)))
		}
	}
}

// ListSessions returns all saved session names.
func (h *Handler) ListSessions(ctx context.Context, req *pb.ListSessionsRequest) (*pb.ListSessionsResponse, error) {
	if h.sessionManager == nil {
		return nil, status.Error(codes.Unavailable, "session management not available")
	}

	sessions, err := h.sessionManager.ListSessions()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list sessions: %v", err)
	}

	if h.sessionMetrics != nil {
		h.sessionMetrics.OperationsTotal.WithLabelValues("list").Inc()
	}
	return &pb.ListSessionsResponse{Sessions: sessions}, nil
}

// LoadSession loads a saved session.
func (h *Handler) LoadSession(ctx context.Context, req *pb.LoadSessionRequest) (*pb.LoadSessionResponse, error) {
	if h.sessionManager == nil {
		return nil, status.Error(codes.Unavailable, "session management not available")
	}

	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "session name cannot be empty")
	}

	// Try v2 first if store supports it
	if v2Store, ok := h.sessionManager.(SessionStoreV2); ok {
		sd, err := v2Store.LoadSessionV2(req.Name)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
		}
		if h.sessionMetrics != nil {
			h.sessionMetrics.OperationsTotal.WithLabelValues("load").Inc()
		}
		return &pb.LoadSessionResponse{
			Messages:    historyToProto(sd.ChatHistory), // v1 compat
			SessionData: modelsSessionDataToProto(sd),   // v2 full data
		}, nil
	}

	// Fallback to v1
	msgs, err := h.sessionManager.LoadSession(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
	}

	if h.sessionMetrics != nil {
		h.sessionMetrics.OperationsTotal.WithLabelValues("load").Inc()
	}
	return &pb.LoadSessionResponse{Messages: historyToProto(msgs)}, nil
}

// SaveSession saves the conversation history.
func (h *Handler) SaveSession(ctx context.Context, req *pb.SaveSessionRequest) (*pb.SaveSessionResponse, error) {
	if h.sessionManager == nil {
		return nil, status.Error(codes.Unavailable, "session management not available")
	}

	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "session name cannot be empty")
	}

	// Prefer v2 if client sent session_data AND store supports v2
	if req.SessionData != nil {
		if v2Store, ok := h.sessionManager.(SessionStoreV2); ok {
			sd := protoSessionDataToModels(req.SessionData)
			if err := v2Store.SaveSessionV2(req.Name, sd); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to save session: %v", err)
			}
			if h.sessionMetrics != nil {
				h.sessionMetrics.OperationsTotal.WithLabelValues("save").Inc()
			}
			return &pb.SaveSessionResponse{Success: true}, nil
		}
	}

	// Fallback to v1
	history := protoToHistory(req.Messages)
	if err := h.sessionManager.SaveSession(req.Name, history); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save session: %v", err)
	}

	if h.sessionMetrics != nil {
		h.sessionMetrics.OperationsTotal.WithLabelValues("save").Inc()
	}
	return &pb.SaveSessionResponse{Success: true}, nil
}

// DeleteSession removes a saved session.
func (h *Handler) DeleteSession(ctx context.Context, req *pb.DeleteSessionRequest) (*pb.DeleteSessionResponse, error) {
	if h.sessionManager == nil {
		return nil, status.Error(codes.Unavailable, "session management not available")
	}

	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "session name cannot be empty")
	}

	if err := h.sessionManager.DeleteSession(req.Name); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete session: %v", err)
	}

	if h.sessionMetrics != nil {
		h.sessionMetrics.OperationsTotal.WithLabelValues("delete").Inc()
	}
	return &pb.DeleteSessionResponse{Success: true}, nil
}

// executeRemoteCommand parses and executes a server-side command, returning the result string.
func (h *Handler) executeRemoteCommand(rawCommand string) string {
	cmd := strings.TrimSpace(rawCommand)
	if cmd == "" {
		return "Error: empty command"
	}

	// Normalize: remove leading '/' if present
	cmd = strings.TrimPrefix(cmd, "/")

	switch {
	case cmd == "status":
		return h.cmdStatus()
	case cmd == "watcher status":
		return h.cmdWatcherStatus()
	case cmd == "plugins list":
		return h.cmdPluginsList()
	case cmd == "agents list":
		return h.cmdAgentsList()
	case cmd == "skills list":
		return h.cmdSkillsList()
	default:
		return fmt.Sprintf("Unknown command: '%s'\n\nAvailable remote commands:\n"+
			"  /status          - Server info (provider, model, version, watcher)\n"+
			"  /watcher status  - K8s watcher status\n"+
			"  /plugins list    - List available plugins\n"+
			"  /agents list     - List available agents\n"+
			"  /skills list     - List available skills",
			rawCommand)
	}
}

func (h *Handler) cmdStatus() string {
	vi := version.GetCurrentVersion()
	var b strings.Builder
	b.WriteString("=== Server Status ===\n")
	b.WriteString(fmt.Sprintf("Version:  %s\n", vi.Version))
	b.WriteString(fmt.Sprintf("Provider: %s\n", h.defaultProvider))
	b.WriteString(fmt.Sprintf("Model:    %s\n", h.defaultModel))

	if h.watcherContextFunc != nil {
		b.WriteString("Watcher:  active")
		if h.watcherDeployment != "" {
			b.WriteString(fmt.Sprintf(" (%s/%s)", h.watcherNamespace, h.watcherDeployment))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("Watcher:  inactive\n")
	}

	if h.pluginManager != nil {
		b.WriteString(fmt.Sprintf("Plugins:  %d loaded\n", len(h.pluginManager.GetPlugins())))
	}
	if h.personaLoader != nil {
		if agents, err := h.personaLoader.ListAgents(); err == nil {
			b.WriteString(fmt.Sprintf("Agents:   %d available\n", len(agents)))
		}
		if skills, err := h.personaLoader.ListSkills(); err == nil {
			b.WriteString(fmt.Sprintf("Skills:   %d available\n", len(skills)))
		}
	}
	if h.fallbackChain != nil {
		b.WriteString("Fallback: configured\n")
	}
	if h.mcpManager != nil {
		b.WriteString("MCP:      active\n")
	}

	return b.String()
}

func (h *Handler) cmdWatcherStatus() string {
	if h.watcherContextFunc == nil {
		return "K8s Watcher: inactive (no watcher configured)"
	}

	var b strings.Builder
	b.WriteString("=== K8s Watcher Status ===\n")
	b.WriteString("Status:     active\n")
	if h.watcherDeployment != "" {
		b.WriteString(fmt.Sprintf("Deployment: %s\n", h.watcherDeployment))
		b.WriteString(fmt.Sprintf("Namespace:  %s\n", h.watcherNamespace))
	}
	if h.watcherStatusFunc != nil {
		b.WriteString(fmt.Sprintf("Summary:    %s\n", h.watcherStatusFunc()))
	}
	if h.watcherStatsFunc != nil {
		alertCount, snapshotCount, podCount := h.watcherStatsFunc()
		b.WriteString(fmt.Sprintf("Alerts:     %d\n", alertCount))
		b.WriteString(fmt.Sprintf("Snapshots:  %d\n", snapshotCount))
		b.WriteString(fmt.Sprintf("Pods:       %d\n", podCount))
	}

	return b.String()
}

func (h *Handler) cmdPluginsList() string {
	if h.pluginManager == nil {
		return "No plugin manager configured on this server."
	}

	plugins := h.pluginManager.GetPlugins()
	if len(plugins) == 0 {
		return "No plugins available."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Plugins (%d) ===\n", len(plugins)))
	for _, p := range plugins {
		b.WriteString(fmt.Sprintf("  - %s", p.Name()))
		if v := p.Version(); v != "" {
			b.WriteString(fmt.Sprintf(" (v%s)", v))
		}
		if d := p.Description(); d != "" {
			b.WriteString(fmt.Sprintf(" — %s", d))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (h *Handler) cmdAgentsList() string {
	if h.personaLoader == nil {
		return "No persona loader configured on this server."
	}

	agents, err := h.personaLoader.ListAgents()
	if err != nil {
		return fmt.Sprintf("Error listing agents: %v", err)
	}
	if len(agents) == 0 {
		return "No agents available."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Agents (%d) ===\n", len(agents)))
	for _, a := range agents {
		b.WriteString(fmt.Sprintf("  - %s", a.Name))
		if a.Description != "" {
			b.WriteString(fmt.Sprintf(" — %s", a.Description))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func (h *Handler) cmdSkillsList() string {
	if h.personaLoader == nil {
		return "No persona loader configured on this server."
	}

	skills, err := h.personaLoader.ListSkills()
	if err != nil {
		return fmt.Sprintf("Error listing skills: %v", err)
	}
	if len(skills) == 0 {
		return "No skills available."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("=== Skills (%d) ===\n", len(skills)))
	for _, s := range skills {
		b.WriteString(fmt.Sprintf("  - %s", s.Name))
		if s.Description != "" {
			b.WriteString(fmt.Sprintf(" — %s", s.Description))
		}
		b.WriteString("\n")
	}

	return b.String()
}
