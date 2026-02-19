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
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`You are a Kubernetes SRE expert. Analyze the following issue and provide a structured assessment with concrete remediation actions.

Issue Details:
- Name: %s
- Namespace: %s
- Resource: %s/%s
- Signal Type: %s
- Severity: %s
- Description: %s
- Risk Score: %d/100`,
		req.IssueName, req.Namespace, req.ResourceKind, req.ResourceName,
		req.SignalType, req.Severity, req.Description, req.RiskScore))

	if req.KubernetesContext != "" {
		sb.WriteString(fmt.Sprintf(`

Kubernetes Cluster Context:
%s`, req.KubernetesContext))
	}

	if req.PreviousFailureContext != "" {
		sb.WriteString(fmt.Sprintf(`

Previous Remediation Attempts (FAILED — you MUST suggest a DIFFERENT strategy):
%s

IMPORTANT: The previous remediation attempts listed above have FAILED. Do NOT repeat the same actions. Analyze why they failed and suggest a fundamentally different approach.`, req.PreviousFailureContext))
	}

	sb.WriteString(`

Available remediation actions (use ONLY these):

1. RestartDeployment — triggers a rolling restart of all pods. No params needed.
   Best for: stale state, memory leaks, transient errors.

2. ScaleDeployment — scales the deployment up or down. Params: {"replicas": "N"} (N >= 1).
   Best for: load-related issues, insufficient capacity.

3. RollbackDeployment — rolls back to a previous deployment revision.
   Params (optional): {"toRevision": "<number|previous|healthy>"}
     "previous" (default): rolls back to revision N-1.
     "healthy": automatically finds the most recent revision with running pods.
     "<number>": rolls back to that specific revision number.
   Best for: bad deployments, image bugs, config regressions.
   IMPORTANT: Use the Revision History in the context to pick the right revision.
   If a specific revision was healthy (readyReplicas > 0), prefer toRevision with that number.

4. AdjustResources — changes CPU/memory requests and limits on a container.
   Params: {"container": "name" (optional, defaults to first), "memory_limit": "1Gi", "memory_request": "512Mi", "cpu_limit": "1000m", "cpu_request": "500m"}
   Provide only the values you want to change. Uses standard K8s notation (Mi, Gi, m for millicores).
   Best for: OOMKilled pods, CPU throttling, resource quota issues.
   Safety: limits cannot be set lower than requests.

5. DeletePod — deletes a single unhealthy pod (the deployment controller recreates it).
   Params (optional): {"pod": "specific-pod-name"}
   If omitted, automatically selects the most-unhealthy pod (CrashLoopBackOff > highest restarts).
   Best for: stuck pods, pods in CrashLoopBackOff that won't recover with restart.
   Safety: refuses if only 1 pod exists, max 1 deletion per action.

6. PatchConfig — updates a ConfigMap. Params: {"configmap": "name", "key1": "value1", "key2": "value2"}.
   Best for: configuration errors, feature flag toggles.

Respond ONLY with a JSON object (no markdown, no code blocks):
{
  "analysis": "Detailed root cause analysis and impact assessment",
  "confidence": 0.85,
  "recommendations": ["First recommendation", "Second recommendation"],
  "actions": [
    {"name": "Increase memory", "action": "AdjustResources", "description": "Pod is OOMKilled, increase memory limit", "params": {"memory_limit": "1Gi", "memory_request": "512Mi"}},
    {"name": "Rollback to healthy", "action": "RollbackDeployment", "description": "Current image is crashing, roll back", "params": {"toRevision": "healthy"}}
  ]
}

Rules:
- confidence: float between 0.0 and 1.0
- recommendations: human-readable text advice
- actions: concrete remediation steps using ONLY the available actions listed above
- Each action must have a description explaining WHY it is recommended
- For OOMKilled issues, ALWAYS consider AdjustResources before restart/rollback
- For CrashLoopBackOff after a recent deploy, prefer RollbackDeployment with toRevision
- Prefer targeted fixes (AdjustResources, specific rollback) over broad actions (restart)`)

	return sb.String()
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

// --- Agentic Remediation ---

// AgenticStep runs one step of the AI-driven remediation loop.
func (h *Handler) AgenticStep(ctx context.Context, req *pb.AgenticStepRequest) (*pb.AgenticStepResponse, error) {
	if req.IssueName == "" {
		return nil, status.Error(codes.InvalidArgument, "issue_name is required")
	}

	llmClient, err := h.getClient(req.Provider, req.Model, "", nil)
	if err != nil {
		h.logger.Error("Failed to get LLM client for agentic step", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "failed to get LLM client: %v", err)
	}

	prompt := buildAgenticStepPrompt(req)
	response, err := llmClient.SendPrompt(ctx, prompt, nil, 0)
	if err != nil {
		h.logger.Error("LLM agentic step failed", zap.Error(err), zap.String("issue", req.IssueName), zap.Int32("step", req.CurrentStep))
		return nil, status.Errorf(codes.Internal, "LLM agentic step failed: %v", err)
	}

	return parseAgenticStepResponse(response), nil
}

func buildAgenticStepPrompt(req *pb.AgenticStepRequest) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf(`You are a Kubernetes SRE agent. You are autonomously remediating an active incident by executing actions one at a time. After each action, you observe the result and decide the next step.

Incident Details:
- Issue: %s
- Namespace: %s
- Resource: %s/%s
- Signal Type: %s
- Severity: %s
- Description: %s
- Risk Score: %d/100`,
		req.IssueName, req.Namespace, req.ResourceKind, req.ResourceName,
		req.SignalType, req.Severity, req.Description, req.RiskScore))

	if req.KubernetesContext != "" {
		sb.WriteString(fmt.Sprintf(`

Current Kubernetes Cluster State (LIVE — refreshed before each step):
%s`, req.KubernetesContext))
	}

	sb.WriteString(`

Available Actions (you can execute ONE per step):

MUTATING (changes the cluster):
1. RestartDeployment — rolling restart of all pods. No params.
2. ScaleDeployment — scale replicas. Params: {"replicas": "N"} (N >= 1).
3. RollbackDeployment — rollback to a previous revision.
   Params: {"toRevision": "previous|healthy|<number>"}
4. AdjustResources — change CPU/memory requests/limits.
   Params: {"container": "name", "memory_limit": "1Gi", "cpu_limit": "500m", ...}
5. DeletePod — delete a single unhealthy pod.
   Params: {"pod": "name"} (optional; auto-selects most-unhealthy if omitted).
6. PatchConfig — update a ConfigMap.
   Params: {"configmap": "name", "key1": "value1", ...}

OBSERVATION (no action, wait for next context refresh):
7. Observe — set next_action to null and resolved to false. Use this when you need to wait and see the effect of a previous action before deciding what to do next.`)

	// Append conversation history
	if len(req.History) > 0 {
		sb.WriteString("\n\nRemediation History:")
		for _, h := range req.History {
			sb.WriteString(fmt.Sprintf("\n\nStep %d:", h.StepNumber))
			sb.WriteString(fmt.Sprintf("\n  AI Reasoning: %s", h.AiMessage))
			if h.Action != "" {
				sb.WriteString(fmt.Sprintf("\n  Action: %s", h.Action))
				if len(h.Params) > 0 {
					sb.WriteString(fmt.Sprintf(" %v", h.Params))
				}
			} else {
				sb.WriteString("\n  Action: (observation only)")
			}
			if h.Observation != "" {
				sb.WriteString(fmt.Sprintf("\n  Observation: %s", h.Observation))
			}
		}
	}

	sb.WriteString(fmt.Sprintf(`

You are on step %d of %d maximum steps.`, req.CurrentStep, req.MaxSteps))

	sb.WriteString(`

Respond ONLY with a JSON object (no markdown, no code blocks):

If the problem is NOT yet resolved:
{
  "reasoning": "Your analysis of the current state and why you choose this action",
  "resolved": false,
  "next_action": {
    "name": "Step description",
    "action": "ActionType",
    "description": "Why this action helps",
    "params": {"key": "value"}
  }
}

If you need to observe (wait for effect of previous action):
{
  "reasoning": "Waiting to observe the effect of the previous action",
  "resolved": false,
  "next_action": null
}

If the problem IS resolved (cluster is healthy):
{
  "reasoning": "Final assessment of what happened and how it was fixed",
  "resolved": true,
  "next_action": null,
  "postmortem_summary": "Brief incident summary for the PostMortem report",
  "root_cause": "The determined root cause of the incident",
  "impact": "What services/users were affected and for how long",
  "lessons_learned": ["Lesson 1", "Lesson 2"],
  "prevention_actions": ["Prevention step 1", "Prevention step 2"]
}

Rules:
- Execute ONE action per step. Observe the result before deciding the next.
- If a previous action FAILED, try a DIFFERENT approach — do not repeat it.
- Only set resolved=true after confirming the cluster state shows healthy pods.
- For OOMKilled, prefer AdjustResources before restart/rollback.
- For CrashLoopBackOff after a recent deploy, prefer RollbackDeployment with toRevision.
- Prefer targeted fixes over broad actions.
- If you cannot determine what to do, set next_action to null (will escalate).
- When resolved, provide thorough postmortem data — you have the full context.`)

	return sb.String()
}

type agenticStepResult struct {
	Reasoning         string       `json:"reasoning"`
	Resolved          bool         `json:"resolved"`
	NextAction        *actionEntry `json:"next_action"`
	PostmortemSummary string       `json:"postmortem_summary"`
	RootCause         string       `json:"root_cause"`
	Impact            string       `json:"impact"`
	LessonsLearned    []string     `json:"lessons_learned"`
	PreventionActions []string     `json:"prevention_actions"`
}

func parseAgenticStepResponse(response string) *pb.AgenticStepResponse {
	cleaned := strings.TrimSpace(response)
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

	var result agenticStepResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		// Parse failure — return safe default (will trigger escalation)
		return &pb.AgenticStepResponse{
			Reasoning: fmt.Sprintf("Failed to parse AI response: %v. Raw: %s", err, response),
			Resolved:  false,
		}
	}

	resp := &pb.AgenticStepResponse{
		Reasoning:         result.Reasoning,
		Resolved:          result.Resolved,
		PostmortemSummary: result.PostmortemSummary,
		RootCause:         result.RootCause,
		Impact:            result.Impact,
		LessonsLearned:    result.LessonsLearned,
		PreventionActions: result.PreventionActions,
	}

	if result.NextAction != nil {
		resp.NextAction = &pb.SuggestedAction{
			Name:        result.NextAction.Name,
			Action:      result.NextAction.Action,
			Description: result.NextAction.Description,
			Params:      result.NextAction.Params,
		}
	}

	return resp
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
