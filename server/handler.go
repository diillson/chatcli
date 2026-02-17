/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/metrics"
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
	watcherAlertsFunc  func() []AlertInfo // raw alerts for AIOps operator
	watcherDeployment  string
	watcherNamespace   string

	// Prometheus metrics (optional, nil when metrics are disabled)
	llmMetrics     *metrics.LLMMetrics
	sessionMetrics *metrics.SessionMetrics
}

// SessionStore abstracts session persistence for testability.
type SessionStore interface {
	SaveSession(name string, history []models.Message) error
	LoadSession(name string) ([]models.Message, error)
	ListSessions() ([]string, error)
	DeleteSession(name string) error
}

// AlertInfo represents a watcher alert exposed to the AIOps operator.
type AlertInfo struct {
	Type       string
	Severity   string
	Message    string
	Object     string
	Namespace  string
	Deployment string
	Timestamp  time.Time
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
	AlertsFunc  func() []AlertInfo                               // raw alerts for AIOps operator
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
	h.watcherAlertsFunc = cfg.AlertsFunc
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
// When metrics are enabled, the returned client is wrapped with instrumentation.
func (h *Handler) getClient(provider, model, clientAPIKey string, providerConfig map[string]string) (client.LLMClient, error) {
	if provider == "" {
		provider = h.defaultProvider
	}
	if model == "" {
		model = h.defaultModel
	}

	var (
		c   client.LLMClient
		err error
	)

	// Client-forwarded credentials with provider-specific config (StackSpot, Ollama, etc.)
	if len(providerConfig) > 0 {
		h.logger.Info("Using client-provided config",
			zap.String("provider", provider),
			zap.Int("config_keys", len(providerConfig)),
		)
		c, err = h.llmManager.CreateClientWithConfig(provider, model, clientAPIKey, providerConfig)
	} else if clientAPIKey != "" {
		// Client-forwarded API key only (OpenAI, Claude, Google, xAI)
		h.logger.Info("Using client-provided API key",
			zap.String("provider", provider),
		)
		c, err = h.llmManager.CreateClientWithKey(provider, model, clientAPIKey)
	} else {
		c, err = h.llmManager.GetClient(provider, model)
	}

	if err != nil {
		return nil, err
	}

	// Wrap with metrics instrumentation if enabled
	if h.llmMetrics != nil {
		return client.NewInstrumentedClient(c, &llmMetricsAdapter{m: h.llmMetrics}, provider), nil
	}

	return c, nil
}

// llmMetricsAdapter bridges metrics.LLMMetrics to client.MetricsRecorder interface.
type llmMetricsAdapter struct {
	m *metrics.LLMMetrics
}

func (a *llmMetricsAdapter) RecordRequest(provider, model, status string, duration time.Duration) {
	a.m.RequestsTotal.WithLabelValues(provider, model, status).Inc()
	a.m.RequestDuration.WithLabelValues(provider, model).Observe(duration.Seconds())
}

func (a *llmMetricsAdapter) RecordError(provider, model, errorType string) {
	a.m.ErrorsTotal.WithLabelValues(provider, model, errorType).Inc()
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

// GetAlerts returns current watcher alerts for the AIOps operator.
func (h *Handler) GetAlerts(ctx context.Context, req *pb.GetAlertsRequest) (*pb.GetAlertsResponse, error) {
	if h.watcherAlertsFunc == nil {
		return &pb.GetAlertsResponse{}, nil
	}

	alerts := h.watcherAlertsFunc()
	var result []*pb.WatcherAlert
	for _, a := range alerts {
		if req.Namespace != "" && a.Namespace != req.Namespace {
			continue
		}
		if req.Deployment != "" && a.Deployment != req.Deployment {
			continue
		}
		result = append(result, &pb.WatcherAlert{
			Type:          a.Type,
			Severity:      a.Severity,
			Message:       a.Message,
			Object:        a.Object,
			Namespace:     a.Namespace,
			Deployment:    a.Deployment,
			TimestampUnix: a.Timestamp.Unix(),
		})
	}

	return &pb.GetAlertsResponse{Alerts: result}, nil
}

// AnalyzeIssue uses the LLM to analyze an AIOps issue and return recommendations.
func (h *Handler) AnalyzeIssue(ctx context.Context, req *pb.AnalyzeIssueRequest) (*pb.AnalyzeIssueResponse, error) {
	if req.IssueName == "" {
		return nil, status.Error(codes.InvalidArgument, "issue_name is required")
	}

	llmClient, err := h.getClient(req.Provider, req.Model, "", nil)
	if err != nil {
		h.logger.Error("Failed to get LLM client for analysis", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get LLM client: %v", err)
	}

	prompt := buildAnalysisPrompt(req)

	// Enrich with K8s context if available
	enrichedPrompt := h.enrichPrompt(prompt)

	response, err := llmClient.SendPrompt(ctx, enrichedPrompt, nil, 0)
	if err != nil {
		h.logger.Error("LLM analysis failed", zap.Error(err), zap.String("issue", req.IssueName))
		return nil, status.Errorf(codes.Internal, "LLM analysis failed: %v", err)
	}

	analysis := parseAnalysisResponse(response)

	provider := req.Provider
	if provider == "" {
		provider = h.defaultProvider
	}

	// Map parsed actions to proto SuggestedAction
	var suggestedActions []*pb.SuggestedAction
	for _, a := range analysis.Actions {
		suggestedActions = append(suggestedActions, &pb.SuggestedAction{
			Name:        a.Name,
			Action:      a.Action,
			Description: a.Description,
			Params:      a.Params,
		})
	}

	return &pb.AnalyzeIssueResponse{
		Analysis:         analysis.Analysis,
		Confidence:       analysis.Confidence,
		Recommendations:  analysis.Recommendations,
		Model:            llmClient.GetModelName(),
		Provider:         provider,
		SuggestedActions: suggestedActions,
	}, nil
}

func buildAnalysisPrompt(req *pb.AnalyzeIssueRequest) string {
	return fmt.Sprintf(`You are a Kubernetes SRE expert. Analyze the following issue and provide a structured assessment with concrete remediation actions.

Issue Details:
- Name: %s
- Namespace: %s
- Resource: %s/%s
- Signal Type: %s
- Severity: %s
- Description: %s
- Risk Score: %d/100

Available remediation actions (use ONLY these):
- RestartDeployment: triggers a rolling restart (no params needed)
- ScaleDeployment: scales the deployment (params: {"replicas": "N"})
- RollbackDeployment: rolls back to the previous revision (no params needed)
- PatchConfig: updates a ConfigMap (params: {"configmap": "name", "key": "value"})

Respond ONLY with a JSON object (no markdown, no code blocks):
{
  "analysis": "Detailed root cause analysis and impact assessment",
  "confidence": 0.85,
  "recommendations": ["First recommendation", "Second recommendation"],
  "actions": [
    {"name": "Restart pods", "action": "RestartDeployment", "description": "Rolling restart to clear stale state", "params": {}},
    {"name": "Scale up", "action": "ScaleDeployment", "description": "Add replicas to handle load", "params": {"replicas": "3"}}
  ]
}

Rules:
- confidence: float between 0.0 and 1.0
- recommendations: human-readable text advice
- actions: concrete remediation steps using ONLY the available actions listed above
- Each action must have a description explaining WHY it is recommended`,
		req.IssueName, req.Namespace, req.ResourceKind, req.ResourceName,
		req.SignalType, req.Severity, req.Description, req.RiskScore)
}

type actionEntry struct {
	Name        string            `json:"name"`
	Action      string            `json:"action"`
	Description string            `json:"description"`
	Params      map[string]string `json:"params,omitempty"`
}

type analysisResult struct {
	Analysis        string        `json:"analysis"`
	Confidence      float32       `json:"confidence"`
	Recommendations []string      `json:"recommendations"`
	Actions         []actionEntry `json:"actions"`
}

func parseAnalysisResponse(response string) analysisResult {
	// Strip markdown code blocks if present
	cleaned := response
	cleaned = strings.TrimSpace(cleaned)
	if strings.HasPrefix(cleaned, "```json") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
		if idx := strings.LastIndex(cleaned, "```"); idx >= 0 {
			cleaned = cleaned[:idx]
		}
	} else if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```")
		if idx := strings.LastIndex(cleaned, "```"); idx >= 0 {
			cleaned = cleaned[:idx]
		}
	}
	cleaned = strings.TrimSpace(cleaned)

	var result analysisResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		// Fallback: treat entire response as analysis text
		return analysisResult{
			Analysis:        response,
			Confidence:      0.5,
			Recommendations: []string{"Review the issue manually — AI response could not be parsed"},
		}
	}

	// Clamp confidence
	if result.Confidence < 0 {
		result.Confidence = 0
	}
	if result.Confidence > 1 {
		result.Confidence = 1
	}

	return result
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
