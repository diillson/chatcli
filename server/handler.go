/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/fallback"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/metrics"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
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

	// Remote resource discovery (optional, nil when not configured)
	pluginManager *plugins.Manager
	personaLoader *persona.Loader

	// Provider fallback chain (optional, nil when not configured)
	fallbackChain *fallback.Chain

	// MCP manager (optional, nil when not configured)
	mcpManager *mcp.Manager
}

// SessionStore abstracts session persistence for testability.
type SessionStore interface {
	SaveSession(name string, history []models.Message) error
	LoadSession(name string) ([]models.Message, error)
	ListSessions() ([]string, error)
	DeleteSession(name string) error
}

// SessionStoreV2 extends SessionStore with v2 scoped-history support.
// Implementations that support v2 are detected via type assertion.
type SessionStoreV2 interface {
	SaveSessionV2(name string, sd *models.SessionData) error
	LoadSessionV2(name string) (*models.SessionData, error)
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
	result := make([]*pb.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		result = append(result, modelMessageToProto(m))
	}
	return result
}

func modelMessageToProto(m models.Message) *pb.ChatMessage {
	msg := &pb.ChatMessage{
		Role:    m.Role,
		Content: m.Content,
	}
	if m.Meta != nil {
		msg.Meta = &pb.MessageMeta{
			IsSummary: m.Meta.IsSummary,
			SummaryOf: int32(m.Meta.SummaryOf),
			Mode:      m.Meta.Mode,
		}
	}
	return msg
}

func protoMessageToModel(cm *pb.ChatMessage) models.Message {
	msg := models.Message{
		Role:    cm.Role,
		Content: cm.Content,
	}
	if cm.Meta != nil {
		msg.Meta = &models.MessageMeta{
			IsSummary: cm.Meta.IsSummary,
			SummaryOf: int(cm.Meta.SummaryOf),
			Mode:      cm.Meta.Mode,
		}
	}
	return msg
}

func modelsToProtoV2(msgs []models.Message) []*pb.ChatMessage {
	result := make([]*pb.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		result = append(result, modelMessageToProto(m))
	}
	return result
}

func protoToModelsV2(msgs []*pb.ChatMessage) []models.Message {
	result := make([]models.Message, 0, len(msgs))
	for _, cm := range msgs {
		result = append(result, protoMessageToModel(cm))
	}
	return result
}

func modelsSessionDataToProto(sd *models.SessionData) *pb.SessionDataV2 {
	return &pb.SessionDataV2{
		Version:      int32(sd.Version),
		ChatHistory:  modelsToProtoV2(sd.ChatHistory),
		AgentHistory: modelsToProtoV2(sd.AgentHistory),
		CoderHistory: modelsToProtoV2(sd.CoderHistory),
		SharedMemory: modelsToProtoV2(sd.SharedMemory),
	}
}

func protoSessionDataToModels(psd *pb.SessionDataV2) *models.SessionData {
	return &models.SessionData{
		Version:      int(psd.Version),
		ChatHistory:  protoToModelsV2(psd.ChatHistory),
		AgentHistory: protoToModelsV2(psd.AgentHistory),
		CoderHistory: protoToModelsV2(psd.CoderHistory),
		SharedMemory: protoToModelsV2(psd.SharedMemory),
	}
}

func (h *Handler) SetPluginManager(pm *plugins.Manager) {
	h.pluginManager = pm
}

// SetPersonaLoader sets the persona loader for remote agent/skill discovery.
func (h *Handler) SetPersonaLoader(pl *persona.Loader) {
	h.personaLoader = pl
}

// SetFallbackChain sets the provider fallback chain for automatic failover.
func (h *Handler) SetFallbackChain(chain *fallback.Chain) {
	h.fallbackChain = chain
}

// SetMCPManager sets the MCP manager for tool interoperability.
func (h *Handler) SetMCPManager(mgr *mcp.Manager) {
	h.mcpManager = mgr
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

	// Resource counts
	if h.pluginManager != nil {
		resp.PluginCount = int32(len(h.pluginManager.GetPlugins()))
	}
	if h.personaLoader != nil {
		if agents, err := h.personaLoader.ListAgents(); err == nil {
			resp.AgentCount = int32(len(agents))
		}
		if skills, err := h.personaLoader.ListSkills(); err == nil {
			resp.SkillCount = int32(len(skills))
		}
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
