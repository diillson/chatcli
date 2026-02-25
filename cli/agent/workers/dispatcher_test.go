package workers

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/token"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// mockLLMClient is a simple mock for testing.
type mockLLMClient struct {
	responses []string
	turn      int
}

func (m *mockLLMClient) GetModelName() string { return "mock" }
func (m *mockLLMClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	if m.turn >= len(m.responses) {
		return "Done. No more actions needed.", nil
	}
	resp := m.responses[m.turn]
	m.turn++
	return resp, nil
}

// mockLLMManager creates mock clients.
type mockLLMManager struct {
	client *mockLLMClient
}

func (m *mockLLMManager) GetClient(_, _ string) (client.LLMClient, error) {
	return &mockLLMClient{responses: m.client.responses}, nil
}
func (m *mockLLMManager) GetAvailableProviders() []string          { return []string{"mock"} }
func (m *mockLLMManager) GetTokenManager() (token.Manager, bool)   { return nil, false }
func (m *mockLLMManager) SetStackSpotRealm(_ string)               {}
func (m *mockLLMManager) SetStackSpotAgentID(_ string)             {}
func (m *mockLLMManager) GetStackSpotRealm() string                { return "" }
func (m *mockLLMManager) GetStackSpotAgentID() string              { return "" }
func (m *mockLLMManager) RefreshProviders()                        {}
func (m *mockLLMManager) CreateClientWithKey(_, _, _ string) (client.LLMClient, error) {
	return m.GetClient("", "")
}
func (m *mockLLMManager) CreateClientWithConfig(_, _, _ string, _ map[string]string) (client.LLMClient, error) {
	return m.GetClient("", "")
}

// mockAgent is a simple agent that counts executions.
type mockAgent struct {
	agentType  AgentType
	execCount  int64
	sleepTime  time.Duration
	returnErr  error
}

func (a *mockAgent) Type() AgentType         { return a.agentType }
func (a *mockAgent) Name() string             { return string(a.agentType) + "-mock" }
func (a *mockAgent) Description() string      { return "Mock agent for testing" }
func (a *mockAgent) SystemPrompt() string     { return "You are a mock agent." }
func (a *mockAgent) Skills() *SkillSet        { return NewSkillSet() }
func (a *mockAgent) AllowedCommands() []string { return []string{"read"} }
func (a *mockAgent) IsReadOnly() bool          { return true }
func (a *mockAgent) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	atomic.AddInt64(&a.execCount, 1)
	if a.sleepTime > 0 {
		select {
		case <-time.After(a.sleepTime):
		case <-ctx.Done():
			return &AgentResult{Error: ctx.Err()}, ctx.Err()
		}
	}
	if a.returnErr != nil {
		return &AgentResult{Error: a.returnErr}, a.returnErr
	}
	return &AgentResult{
		Output: fmt.Sprintf("Mock output for: %s", task),
	}, nil
}

func TestDispatcher_SequentialExecution(t *testing.T) {
	logger := zap.NewNop()
	registry := NewRegistry()
	fileAgent := &mockAgent{agentType: AgentTypeFile}
	coderAgent := &mockAgent{agentType: AgentTypeCoder}
	registry.Register(fileAgent)
	registry.Register(coderAgent)

	mgr := &mockLLMManager{client: &mockLLMClient{responses: []string{"done"}}}

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:    4,
		ParallelMode:  false, // sequential
		WorkerTimeout: 5 * time.Second,
	}, logger)

	calls := []AgentCall{
		{Agent: AgentTypeFile, Task: "read files", ID: "1"},
		{Agent: AgentTypeCoder, Task: "write code", ID: "2"},
	}

	results := dispatcher.Dispatch(context.Background(), calls)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Error != nil {
		t.Errorf("first result error: %v", results[0].Error)
	}
	if results[1].Error != nil {
		t.Errorf("second result error: %v", results[1].Error)
	}
}

func TestDispatcher_ParallelExecution(t *testing.T) {
	logger := zap.NewNop()
	registry := NewRegistry()

	// Agents that sleep briefly to verify parallel execution
	fileAgent := &mockAgent{agentType: AgentTypeFile, sleepTime: 50 * time.Millisecond}
	searchAgent := &mockAgent{agentType: AgentTypeSearch, sleepTime: 50 * time.Millisecond}
	registry.Register(fileAgent)
	registry.Register(searchAgent)

	mgr := &mockLLMManager{client: &mockLLMClient{responses: []string{"done"}}}

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:    4,
		ParallelMode:  true,
		WorkerTimeout: 5 * time.Second,
	}, logger)

	calls := []AgentCall{
		{Agent: AgentTypeFile, Task: "read files", ID: "1"},
		{Agent: AgentTypeSearch, Task: "search code", ID: "2"},
	}

	start := time.Now()
	results := dispatcher.Dispatch(context.Background(), calls)
	elapsed := time.Since(start)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// If parallel, total time should be roughly 50ms, not 100ms
	if elapsed > 150*time.Millisecond {
		t.Errorf("parallel execution took too long: %v (expected ~50ms)", elapsed)
	}
}

func TestDispatcher_UnknownAgent(t *testing.T) {
	logger := zap.NewNop()
	registry := NewRegistry()
	mgr := &mockLLMManager{client: &mockLLMClient{responses: []string{"done"}}}

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:    4,
		ParallelMode:  true,
		WorkerTimeout: 5 * time.Second,
	}, logger)

	calls := []AgentCall{
		{Agent: "nonexistent", Task: "do something", ID: "1"},
	}

	results := dispatcher.Dispatch(context.Background(), calls)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Error("expected error for unknown agent")
	}
}

func TestDispatcher_ContextCancellation(t *testing.T) {
	logger := zap.NewNop()
	registry := NewRegistry()
	slowAgent := &mockAgent{agentType: AgentTypeFile, sleepTime: 5 * time.Second}
	registry.Register(slowAgent)

	mgr := &mockLLMManager{client: &mockLLMClient{responses: []string{"done"}}}

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:    4,
		ParallelMode:  true,
		WorkerTimeout: 10 * time.Second,
	}, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	calls := []AgentCall{
		{Agent: AgentTypeFile, Task: "slow task", ID: "1"},
	}

	results := dispatcher.Dispatch(ctx, calls)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Error("expected error from context cancellation")
	}
}

func TestDispatcher_MaxWorkersSemaphore(t *testing.T) {
	logger := zap.NewNop()
	registry := NewRegistry()

	var maxConcurrent int64
	var current int64

	countingAgent := &mockAgent{agentType: AgentTypeFile}
	// Override Execute to track concurrency
	registry.Register(&concurrencyTracker{
		agentType:      AgentTypeFile,
		current:        &current,
		maxConcurrent:  &maxConcurrent,
		sleepTime:      50 * time.Millisecond,
	})

	mgr := &mockLLMManager{client: &mockLLMClient{responses: []string{"done"}}}

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:    2, // limit to 2
		ParallelMode:  true,
		WorkerTimeout: 5 * time.Second,
	}, logger)

	// Dispatch 5 calls
	calls := make([]AgentCall, 5)
	for i := range calls {
		calls[i] = AgentCall{Agent: AgentTypeFile, Task: fmt.Sprintf("task %d", i), ID: fmt.Sprintf("%d", i)}
	}

	_ = dispatcher.Dispatch(context.Background(), calls)
	_ = countingAgent

	if atomic.LoadInt64(&maxConcurrent) > 2 {
		t.Errorf("max concurrent workers exceeded limit: got %d, expected <= 2", maxConcurrent)
	}
}

// concurrencyTracker tracks max concurrent executions.
type concurrencyTracker struct {
	agentType     AgentType
	current       *int64
	maxConcurrent *int64
	sleepTime     time.Duration
}

func (a *concurrencyTracker) Type() AgentType         { return a.agentType }
func (a *concurrencyTracker) Name() string             { return "tracker" }
func (a *concurrencyTracker) Description() string      { return "Tracks concurrency" }
func (a *concurrencyTracker) SystemPrompt() string     { return "" }
func (a *concurrencyTracker) Skills() *SkillSet        { return NewSkillSet() }
func (a *concurrencyTracker) AllowedCommands() []string { return nil }
func (a *concurrencyTracker) IsReadOnly() bool          { return true }
func (a *concurrencyTracker) Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error) {
	c := atomic.AddInt64(a.current, 1)
	// Update max
	for {
		old := atomic.LoadInt64(a.maxConcurrent)
		if c <= old {
			break
		}
		if atomic.CompareAndSwapInt64(a.maxConcurrent, old, c) {
			break
		}
	}

	time.Sleep(a.sleepTime)
	atomic.AddInt64(a.current, -1)

	return &AgentResult{Output: "tracked"}, nil
}

func TestFormatResults(t *testing.T) {
	results := []AgentResult{
		{
			CallID:   "ac-1",
			Agent:    AgentTypeFile,
			Task:     "read files",
			Output:   "file contents here",
			Duration: 150 * time.Millisecond,
		},
		{
			CallID:   "ac-2",
			Agent:    AgentTypeSearch,
			Task:     "search for X",
			Error:    fmt.Errorf("search failed"),
			Duration: 50 * time.Millisecond,
		},
	}

	formatted := FormatResults(results)
	if formatted == "" {
		t.Fatal("expected non-empty formatted results")
	}
	if !containsSubstr(formatted, "[file]") {
		t.Error("expected [file] in formatted output")
	}
	if !containsSubstr(formatted, "FAILED") {
		t.Error("expected FAILED in formatted output")
	}
	if !containsSubstr(formatted, "file contents here") {
		t.Error("expected output content in formatted result")
	}
}

func TestDispatcher_EmptyCalls(t *testing.T) {
	logger := zap.NewNop()
	registry := NewRegistry()
	mgr := &mockLLMManager{client: &mockLLMClient{}}

	dispatcher := NewDispatcher(registry, mgr, DispatcherConfig{
		MaxWorkers:   4,
		ParallelMode: true,
	}, logger)

	results := dispatcher.Dispatch(context.Background(), nil)
	if results != nil {
		t.Errorf("expected nil results for empty calls, got %v", results)
	}
}
