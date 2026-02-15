/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package server

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// streamChunkSize is the approximate number of characters per streaming chunk.
	// Splitting at sentence/paragraph boundaries gives clients a natural progressive display.
	streamChunkSize = 200
)

// Handler implements the ChatCLIService gRPC server.
type Handler struct {
	pb.UnimplementedChatCLIServiceServer

	llmManager     manager.LLMManager
	sessionManager SessionStore
	logger         *zap.Logger

	// default provider/model from server config
	defaultProvider string
	defaultModel    string

	// K8s watcher context injection (optional, set when watcher is active)
	watcherContextFunc func() string
	watcherStatusFunc  func() string // compact status summary
	watcherStatsFunc   func() (alertCount, snapshotCount, podCount int)
	watcherDeployment  string
	watcherNamespace   string
}

// SessionStore abstracts session persistence for testability.
type SessionStore interface {
	SaveSession(name string, history []models.Message) error
	LoadSession(name string) ([]models.Message, error)
	ListSessions() ([]string, error)
	DeleteSession(name string) error
}

// NewHandler creates a new gRPC handler.
func NewHandler(llmMgr manager.LLMManager, sessionStore SessionStore, logger *zap.Logger, provider, model string) *Handler {
	return &Handler{
		llmManager:      llmMgr,
		sessionManager:  sessionStore,
		logger:          logger,
		defaultProvider: provider,
		defaultModel:    model,
	}
}

// WatcherConfig holds the functions and metadata for K8s watcher integration.
type WatcherConfig struct {
	ContextFunc func() string                                    // full context for LLM
	StatusFunc  func() string                                    // compact status summary
	StatsFunc   func() (alertCount, snapshotCount, podCount int) // numeric stats
	Deployment  string
	Namespace   string
}

// SetWatcherContext configures a function that provides K8s watcher context
// to be prepended to all LLM prompts.
func (h *Handler) SetWatcherContext(fn func() string) {
	h.watcherContextFunc = fn
}

// SetWatcher configures full watcher integration with context, status, and stats.
func (h *Handler) SetWatcher(cfg WatcherConfig) {
	h.watcherContextFunc = cfg.ContextFunc
	h.watcherStatusFunc = cfg.StatusFunc
	h.watcherStatsFunc = cfg.StatsFunc
	h.watcherDeployment = cfg.Deployment
	h.watcherNamespace = cfg.Namespace
}

// enrichPrompt prepends K8s watcher context to the prompt if available.
func (h *Handler) enrichPrompt(prompt string) string {
	if h.watcherContextFunc == nil {
		return prompt
	}
	ctx := h.watcherContextFunc()
	if ctx == "" {
		return prompt
	}
	return ctx + "\n\nUser Question: " + prompt
}

// getClient resolves the LLM client to use, optionally overriding provider/model.
// If clientAPIKey is non-empty or providerConfig has entries, creates a new client
// using the caller's credentials instead of the server's default ones.
func (h *Handler) getClient(provider, model, clientAPIKey string, providerConfig map[string]string) (client.LLMClient, error) {
	if provider == "" {
		provider = h.defaultProvider
	}
	if model == "" {
		model = h.defaultModel
	}

	// Client-forwarded credentials with provider-specific config (StackSpot, Ollama, etc.)
	if len(providerConfig) > 0 {
		h.logger.Info("Using client-provided config",
			zap.String("provider", provider),
			zap.Int("config_keys", len(providerConfig)),
		)
		return h.llmManager.CreateClientWithConfig(provider, model, clientAPIKey, providerConfig)
	}

	// Client-forwarded API key only (OpenAI, Claude, Google, xAI)
	if clientAPIKey != "" {
		h.logger.Info("Using client-provided API key",
			zap.String("provider", provider),
		)
		return h.llmManager.CreateClientWithKey(provider, model, clientAPIKey)
	}

	return h.llmManager.GetClient(provider, model)
}

func protoToHistory(msgs []*pb.ChatMessage) []models.Message {
	history := make([]models.Message, 0, len(msgs))
	for _, m := range msgs {
		history = append(history, models.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	return history
}

func historyToProto(msgs []models.Message) []*pb.ChatMessage {
	out := make([]*pb.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, &pb.ChatMessage{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	return out
}

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

	// Stream the response in chunks for progressive client display.
	// When LLMClient gains native streaming, this can be replaced with
	// real token-by-token forwarding.
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
			// Handle CLI commands forwarded from the client
			if err := stream.Send(&pb.SessionMessage{
				Type:    pb.SessionMessage_COMMAND_RESULT,
				Content: fmt.Sprintf("Command '%s' received (remote command execution not yet implemented)", msg.Content),
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

	msgs, err := h.sessionManager.LoadSession(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "session not found: %v", err)
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

	history := protoToHistory(req.Messages)
	if err := h.sessionManager.SaveSession(req.Name, history); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save session: %v", err)
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

	return &pb.DeleteSessionResponse{Success: true}, nil
}

// GetServerInfo returns server metadata.
func (h *Handler) GetServerInfo(ctx context.Context, req *pb.GetServerInfoRequest) (*pb.GetServerInfoResponse, error) {
	vi := version.GetCurrentVersion()
	resp := &pb.GetServerInfoResponse{
		Version:            vi.Version,
		Provider:           h.defaultProvider,
		Model:              h.defaultModel,
		AvailableProviders: h.llmManager.GetAvailableProviders(),
		WatcherActive:      h.watcherContextFunc != nil,
	}
	if h.watcherDeployment != "" {
		resp.WatcherTarget = h.watcherNamespace + "/" + h.watcherDeployment
	}
	return resp, nil
}

// GetWatcherStatus returns the K8s watcher status.
func (h *Handler) GetWatcherStatus(ctx context.Context, req *pb.GetWatcherStatusRequest) (*pb.GetWatcherStatusResponse, error) {
	resp := &pb.GetWatcherStatusResponse{
		Active:     h.watcherContextFunc != nil,
		Deployment: h.watcherDeployment,
		Namespace:  h.watcherNamespace,
	}

	if h.watcherStatusFunc != nil {
		resp.StatusSummary = h.watcherStatusFunc()
	}
	if h.watcherStatsFunc != nil {
		alertCount, snapshotCount, podCount := h.watcherStatsFunc()
		resp.AlertCount = int32(alertCount)
		resp.SnapshotCount = int32(snapshotCount)
		resp.PodCount = int32(podCount)
	}

	return resp, nil
}

// Health returns the server health status.
func (h *Handler) Health(ctx context.Context, req *pb.HealthRequest) (*pb.HealthResponse, error) {
	vi := version.GetCurrentVersion()
	return &pb.HealthResponse{
		Status:  pb.HealthResponse_SERVING,
		Version: vi.Version,
	}, nil
}

// chunkResponse splits a response into chunks at natural boundaries (newlines,
// sentence endings) for progressive streaming to the client. Each chunk is
// approximately targetSize characters, but boundaries are respected to avoid
// splitting mid-word or mid-sentence.
func chunkResponse(text string, targetSize int) []string {
	if len(text) <= targetSize {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= targetSize {
			chunks = append(chunks, remaining)
			break
		}

		// Look for a natural break point within the target window
		end := targetSize
		if end > len(remaining) {
			end = len(remaining)
		}

		// Try paragraph break first (\n\n)
		if idx := strings.LastIndex(remaining[:end], "\n\n"); idx > 0 {
			chunks = append(chunks, remaining[:idx+2])
			remaining = remaining[idx+2:]
			continue
		}

		// Try line break (\n)
		if idx := strings.LastIndex(remaining[:end], "\n"); idx > 0 {
			chunks = append(chunks, remaining[:idx+1])
			remaining = remaining[idx+1:]
			continue
		}

		// Try sentence end (. ! ?)
		bestBreak := -1
		for _, sep := range []string{". ", "! ", "? "} {
			if idx := strings.LastIndex(remaining[:end], sep); idx > bestBreak {
				bestBreak = idx + len(sep)
			}
		}
		if bestBreak > 0 {
			chunks = append(chunks, remaining[:bestBreak])
			remaining = remaining[bestBreak:]
			continue
		}

		// Try space
		if idx := strings.LastIndex(remaining[:end], " "); idx > 0 {
			chunks = append(chunks, remaining[:idx+1])
			remaining = remaining[idx+1:]
			continue
		}

		// No break found, hard split
		chunks = append(chunks, remaining[:end])
		remaining = remaining[end:]
	}

	return chunks
}
