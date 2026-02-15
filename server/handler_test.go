package server

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"go.uber.org/zap"
)

// --- Mock LLMClient (implements client.LLMClient) ---

type mockLLMClient struct {
	mock.Mock
}

func (m *mockLLMClient) GetModelName() string {
	args := m.Called()
	return args.String(0)
}

func (m *mockLLMClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	args := m.Called(ctx, prompt, history, maxTokens)
	return args.String(0), args.Error(1)
}

// --- Mock LLMManager (implements manager.LLMManager) ---

type mockLLMManager struct {
	mock.Mock
}

func (m *mockLLMManager) GetClient(provider string, model string) (client.LLMClient, error) {
	args := m.Called(provider, model)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(client.LLMClient), args.Error(1)
}

func (m *mockLLMManager) GetAvailableProviders() []string {
	args := m.Called()
	return args.Get(0).([]string)
}

func (m *mockLLMManager) GetTokenManager() (token.Manager, bool) { return nil, false }
func (m *mockLLMManager) SetStackSpotRealm(realm string)         {}
func (m *mockLLMManager) SetStackSpotAgentID(agentID string)     {}
func (m *mockLLMManager) GetStackSpotRealm() string              { return "" }
func (m *mockLLMManager) GetStackSpotAgentID() string            { return "" }
func (m *mockLLMManager) RefreshProviders()                      {}
func (m *mockLLMManager) CreateClientWithKey(provider, model, apiKey string) (client.LLMClient, error) {
	args := m.Called(provider, model, apiKey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(client.LLMClient), args.Error(1)
}
func (m *mockLLMManager) CreateClientWithConfig(provider, model, apiKey string, providerConfig map[string]string) (client.LLMClient, error) {
	args := m.Called(provider, model, apiKey, providerConfig)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(client.LLMClient), args.Error(1)
}

// --- Mock SessionStore ---

type mockSessionStore struct {
	mock.Mock
}

func (m *mockSessionStore) SaveSession(name string, history []models.Message) error {
	args := m.Called(name, history)
	return args.Error(0)
}

func (m *mockSessionStore) LoadSession(name string) ([]models.Message, error) {
	args := m.Called(name)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]models.Message), args.Error(1)
}

func (m *mockSessionStore) ListSessions() ([]string, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

func (m *mockSessionStore) DeleteSession(name string) error {
	args := m.Called(name)
	return args.Error(0)
}

// --- Tests ---

func TestHandler_Health(t *testing.T) {
	logger := zap.NewNop()
	handler := &Handler{
		logger:          logger,
		defaultProvider: "openai",
		defaultModel:    "gpt-4",
	}

	resp, err := handler.Health(context.Background(), &pb.HealthRequest{})
	assert.NoError(t, err)
	assert.Equal(t, pb.HealthResponse_SERVING, resp.Status)
}

func TestHandler_GetServerInfo(t *testing.T) {
	logger := zap.NewNop()
	mgr := &mockLLMManager{}
	mgr.On("GetAvailableProviders").Return([]string{"openai", "anthropic"})

	handler := NewHandler(mgr, nil, logger, "openai", "gpt-4")

	resp, err := handler.GetServerInfo(context.Background(), &pb.GetServerInfoRequest{})
	assert.NoError(t, err)
	assert.Equal(t, "openai", resp.Provider)
	assert.Equal(t, "gpt-4", resp.Model)
	assert.Contains(t, resp.AvailableProviders, "openai")
	assert.Contains(t, resp.AvailableProviders, "anthropic")
}

func TestHandler_SendPrompt_EmptyPrompt(t *testing.T) {
	logger := zap.NewNop()
	handler := &Handler{logger: logger}

	_, err := handler.SendPrompt(context.Background(), &pb.SendPromptRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "prompt cannot be empty")
}

func TestHandler_SendPrompt_Success(t *testing.T) {
	logger := zap.NewNop()

	llmClient := &mockLLMClient{}
	llmClient.On("SendPrompt", mock.Anything, "Hello", []models.Message{}, 0).Return("Hi there!", nil)
	llmClient.On("GetModelName").Return("gpt-4")

	mgr := &mockLLMManager{}
	mgr.On("GetClient", "openai", "").Return(llmClient, nil)

	handler := NewHandler(mgr, nil, logger, "openai", "")

	resp, err := handler.SendPrompt(context.Background(), &pb.SendPromptRequest{
		Prompt: "Hello",
	})
	assert.NoError(t, err)
	assert.Equal(t, "Hi there!", resp.Response)
	assert.Equal(t, "gpt-4", resp.Model)
}

func TestHandler_SendPrompt_WithClientAPIKey(t *testing.T) {
	logger := zap.NewNop()

	llmClient := &mockLLMClient{}
	llmClient.On("SendPrompt", mock.Anything, "Hello", []models.Message{}, 0).Return("Using your key!", nil)
	llmClient.On("GetModelName").Return("gpt-4o")

	mgr := &mockLLMManager{}
	mgr.On("CreateClientWithKey", "OPENAI", "", "sk-user-key-123").Return(llmClient, nil)

	handler := NewHandler(mgr, nil, logger, "OPENAI", "")

	resp, err := handler.SendPrompt(context.Background(), &pb.SendPromptRequest{
		Prompt:       "Hello",
		ClientApiKey: "sk-user-key-123",
	})
	assert.NoError(t, err)
	assert.Equal(t, "Using your key!", resp.Response)
	assert.Equal(t, "gpt-4o", resp.Model)
	mgr.AssertCalled(t, "CreateClientWithKey", "OPENAI", "", "sk-user-key-123")
}

func TestHandler_ListSessions(t *testing.T) {
	logger := zap.NewNop()
	store := &mockSessionStore{}
	store.On("ListSessions").Return([]string{"session1", "session2"}, nil)

	handler := &Handler{
		sessionManager: store,
		logger:         logger,
	}

	resp, err := handler.ListSessions(context.Background(), &pb.ListSessionsRequest{})
	assert.NoError(t, err)
	assert.Equal(t, []string{"session1", "session2"}, resp.Sessions)
}

func TestHandler_SaveAndLoadSession(t *testing.T) {
	logger := zap.NewNop()
	store := &mockSessionStore{}

	history := []models.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	store.On("SaveSession", "test", history).Return(nil)
	store.On("LoadSession", "test").Return(history, nil)

	handler := &Handler{
		sessionManager: store,
		logger:         logger,
	}

	// Save
	saveResp, err := handler.SaveSession(context.Background(), &pb.SaveSessionRequest{
		Name: "test",
		Messages: []*pb.ChatMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi"},
		},
	})
	assert.NoError(t, err)
	assert.True(t, saveResp.Success)

	// Load
	loadResp, err := handler.LoadSession(context.Background(), &pb.LoadSessionRequest{Name: "test"})
	assert.NoError(t, err)
	assert.Len(t, loadResp.Messages, 2)
	assert.Equal(t, "user", loadResp.Messages[0].Role)
	assert.Equal(t, "hello", loadResp.Messages[0].Content)
}

func TestHandler_DeleteSession(t *testing.T) {
	logger := zap.NewNop()
	store := &mockSessionStore{}
	store.On("DeleteSession", "old-session").Return(nil)

	handler := &Handler{
		sessionManager: store,
		logger:         logger,
	}

	resp, err := handler.DeleteSession(context.Background(), &pb.DeleteSessionRequest{Name: "old-session"})
	assert.NoError(t, err)
	assert.True(t, resp.Success)
}

func TestHandler_NoSessionManager(t *testing.T) {
	logger := zap.NewNop()
	handler := &Handler{
		sessionManager: nil,
		logger:         logger,
	}

	_, err := handler.ListSessions(context.Background(), &pb.ListSessionsRequest{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "session management not available")
}

func TestProtoToHistory(t *testing.T) {
	msgs := []*pb.ChatMessage{
		{Role: "user", Content: "test"},
		{Role: "assistant", Content: "response"},
	}

	history := protoToHistory(msgs)
	assert.Len(t, history, 2)
	assert.Equal(t, "user", history[0].Role)
	assert.Equal(t, "test", history[0].Content)
	assert.Equal(t, "assistant", history[1].Role)
}

func TestHistoryToProto(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", Content: "test"},
		{Role: "assistant", Content: "response"},
	}

	proto := historyToProto(msgs)
	assert.Len(t, proto, 2)
	assert.Equal(t, "user", proto[0].Role)
	assert.Equal(t, "response", proto[1].Content)
}

func TestHandler_EnrichPrompt_NoWatcher(t *testing.T) {
	handler := &Handler{logger: zap.NewNop()}
	result := handler.enrichPrompt("What's the status?")
	assert.Equal(t, "What's the status?", result)
}

func TestHandler_EnrichPrompt_WithWatcher(t *testing.T) {
	handler := &Handler{logger: zap.NewNop()}
	handler.SetWatcherContext(func() string {
		return "[K8s Context: deployment/myapp]\nPods: 3/3 ready"
	})

	result := handler.enrichPrompt("Is it healthy?")
	assert.Contains(t, result, "[K8s Context: deployment/myapp]")
	assert.Contains(t, result, "Pods: 3/3 ready")
	assert.Contains(t, result, "User Question: Is it healthy?")
}

func TestHandler_EnrichPrompt_EmptyContext(t *testing.T) {
	handler := &Handler{logger: zap.NewNop()}
	handler.SetWatcherContext(func() string { return "" })

	result := handler.enrichPrompt("Hello")
	assert.Equal(t, "Hello", result)
}

func TestHandler_SendPrompt_WithWatcherContext(t *testing.T) {
	logger := zap.NewNop()

	llmClient := &mockLLMClient{}
	// The enriched prompt should contain the K8s context + user question
	llmClient.On("SendPrompt", mock.Anything, mock.MatchedBy(func(prompt string) bool {
		return strings.Contains(prompt, "[K8s] 3 pods ready") && strings.Contains(prompt, "User Question: Is it healthy?")
	}), []models.Message{}, 0).Return("Yes, all pods are running.", nil)
	llmClient.On("GetModelName").Return("gpt-4")

	mgr := &mockLLMManager{}
	mgr.On("GetClient", "openai", "").Return(llmClient, nil)

	handler := NewHandler(mgr, nil, logger, "openai", "")
	handler.SetWatcherContext(func() string {
		return "[K8s] 3 pods ready"
	})

	resp, err := handler.SendPrompt(context.Background(), &pb.SendPromptRequest{
		Prompt: "Is it healthy?",
	})
	assert.NoError(t, err)
	assert.Equal(t, "Yes, all pods are running.", resp.Response)
}

func TestHandler_GetServerInfo_WithWatcher(t *testing.T) {
	logger := zap.NewNop()
	mgr := &mockLLMManager{}
	mgr.On("GetAvailableProviders").Return([]string{"openai"})

	handler := NewHandler(mgr, nil, logger, "openai", "gpt-4")
	handler.SetWatcher(WatcherConfig{
		ContextFunc: func() string { return "ctx" },
		StatusFunc:  func() string { return "3/3 pods ready" },
		Deployment:  "myapp",
		Namespace:   "production",
	})

	resp, err := handler.GetServerInfo(context.Background(), &pb.GetServerInfoRequest{})
	assert.NoError(t, err)
	assert.True(t, resp.WatcherActive)
	assert.Equal(t, "production/myapp", resp.WatcherTarget)
}

func TestHandler_GetServerInfo_NoWatcher(t *testing.T) {
	logger := zap.NewNop()
	mgr := &mockLLMManager{}
	mgr.On("GetAvailableProviders").Return([]string{"openai"})

	handler := NewHandler(mgr, nil, logger, "openai", "gpt-4")

	resp, err := handler.GetServerInfo(context.Background(), &pb.GetServerInfoRequest{})
	assert.NoError(t, err)
	assert.False(t, resp.WatcherActive)
	assert.Empty(t, resp.WatcherTarget)
}

func TestHandler_GetWatcherStatus_Active(t *testing.T) {
	logger := zap.NewNop()
	handler := &Handler{logger: logger}
	handler.SetWatcher(WatcherConfig{
		ContextFunc: func() string { return "context" },
		StatusFunc:  func() string { return "healthy: 3/3 pods" },
		StatsFunc:   func() (int, int, int) { return 2, 10, 3 },
		Deployment:  "myapp",
		Namespace:   "production",
	})

	resp, err := handler.GetWatcherStatus(context.Background(), &pb.GetWatcherStatusRequest{})
	assert.NoError(t, err)
	assert.True(t, resp.Active)
	assert.Equal(t, "myapp", resp.Deployment)
	assert.Equal(t, "production", resp.Namespace)
	assert.Equal(t, "healthy: 3/3 pods", resp.StatusSummary)
	assert.Equal(t, int32(2), resp.AlertCount)
	assert.Equal(t, int32(10), resp.SnapshotCount)
	assert.Equal(t, int32(3), resp.PodCount)
}

func TestHandler_GetWatcherStatus_Inactive(t *testing.T) {
	logger := zap.NewNop()
	handler := &Handler{logger: logger}

	resp, err := handler.GetWatcherStatus(context.Background(), &pb.GetWatcherStatusRequest{})
	assert.NoError(t, err)
	assert.False(t, resp.Active)
	assert.Empty(t, resp.Deployment)
}

func TestChunkResponse_ShortText(t *testing.T) {
	chunks := chunkResponse("Hello world", 200)
	assert.Len(t, chunks, 1)
	assert.Equal(t, "Hello world", chunks[0])
}

func TestChunkResponse_ParagraphBreak(t *testing.T) {
	text := strings.Repeat("word ", 30) + "\n\n" + strings.Repeat("more ", 30)
	chunks := chunkResponse(text, 200)
	assert.Greater(t, len(chunks), 1)
	// First chunk should end at paragraph break
	assert.True(t, strings.HasSuffix(chunks[0], "\n\n"))
}

func TestChunkResponse_SentenceBreak(t *testing.T) {
	text := "This is the first sentence. This is the second sentence. " + strings.Repeat("word ", 50)
	chunks := chunkResponse(text, 80)
	assert.Greater(t, len(chunks), 1)
}

func TestChunkResponse_ReassemblesCorrectly(t *testing.T) {
	text := "Hello world. This is a test of the chunking system.\n\nNew paragraph here. More text follows after this sentence. " + strings.Repeat("x", 300)
	chunks := chunkResponse(text, 100)
	reassembled := strings.Join(chunks, "")
	assert.Equal(t, text, reassembled)
}

func TestChunkResponse_EmptyString(t *testing.T) {
	chunks := chunkResponse("", 200)
	assert.Len(t, chunks, 1)
	assert.Equal(t, "", chunks[0])
}

func TestChunkResponse_LargeChunks(t *testing.T) {
	text := strings.Repeat("abcdefghij ", 100) // ~1100 chars
	chunks := chunkResponse(text, 200)
	assert.Greater(t, len(chunks), 3)
	// Verify no data lost
	reassembled := strings.Join(chunks, "")
	assert.Equal(t, text, reassembled)
}
