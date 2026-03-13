package fallback

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// --- Mock clients ---

// mockClient implements client.LLMClient.
type mockClient struct {
	model    string
	response string
	err      error
	calls    int
}

func (m *mockClient) GetModelName() string { return m.model }
func (m *mockClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	m.calls++
	return m.response, m.err
}

// mockToolClient implements both LLMClient and ToolAwareClient.
type mockToolClient struct {
	mockClient
	toolResponse *models.LLMResponse
	toolErr      error
	toolCalls    int
}

func (m *mockToolClient) SendPromptWithTools(_ context.Context, _ string, _ []models.Message, _ []models.ToolDefinition, _ int) (*models.LLMResponse, error) {
	m.toolCalls++
	return m.toolResponse, m.toolErr
}

func (m *mockToolClient) SupportsNativeTools() bool { return true }

// --- Helpers ---

func newTestLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

func makeEntry(provider, model string, c *mockClient) FallbackEntry {
	return FallbackEntry{
		Provider: provider,
		Model:    model,
		Client:   c,
	}
}

func makeToolEntry(provider, model string, c *mockToolClient) FallbackEntry {
	return FallbackEntry{
		Provider: provider,
		Model:    model,
		Client:   c,
	}
}

// --- Tests ---

func TestSendPrompt_SuccessOnFirstProvider(t *testing.T) {
	primary := &mockClient{model: "gpt-4", response: "hello from primary"}
	secondary := &mockClient{model: "claude-3", response: "hello from secondary"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	})

	resp, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "hello from primary" {
		t.Fatalf("expected primary response, got %q", resp)
	}
	if primary.calls != 1 {
		t.Fatalf("expected primary called once, got %d", primary.calls)
	}
	if secondary.calls != 0 {
		t.Fatalf("expected secondary not called, got %d", secondary.calls)
	}
}

func TestSendPrompt_FallbackOnPrimaryFailure(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("500 internal server error")}
	secondary := &mockClient{model: "claude-3", response: "hello from secondary"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(0)) // no retries, fall through immediately

	resp, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "hello from secondary" {
		t.Fatalf("expected secondary response, got %q", resp)
	}
	if primary.calls != 1 {
		t.Fatalf("expected primary called once, got %d", primary.calls)
	}
	if secondary.calls != 1 {
		t.Fatalf("expected secondary called once, got %d", secondary.calls)
	}
}

func TestSendPrompt_AllProvidersFail(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("500 internal server error")}
	secondary := &mockClient{model: "claude-3", err: errors.New("502 bad gateway")}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(0))

	resp, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if resp != "" {
		t.Fatalf("expected empty response, got %q", resp)
	}
	if !errors.Is(err, secondary.err) {
		t.Fatalf("expected wrapped last error, got: %v", err)
	}
}

func TestSendPrompt_NoProviders(t *testing.T) {
	chain := NewChain(newTestLogger(), nil)

	_, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err == nil {
		t.Fatal("expected error with no providers")
	}
}

func TestSendPrompt_AuthErrorSkipsRetries(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("401 unauthorized")}
	secondary := &mockClient{model: "claude-3", response: "ok"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(5))

	resp, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected secondary response, got %q", resp)
	}
	// Auth error should not be retried: primary called exactly once
	if primary.calls != 1 {
		t.Fatalf("expected primary called once (no retries for auth), got %d", primary.calls)
	}
}

func TestSendPrompt_ModelNotFoundSkipsRetries(t *testing.T) {
	primary := &mockClient{model: "gpt-x", err: errors.New("model not found")}
	secondary := &mockClient{model: "claude-3", response: "ok"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-x", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(5))

	resp, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected secondary response, got %q", resp)
	}
	if primary.calls != 1 {
		t.Fatalf("expected primary called once (no retries for model not found), got %d", primary.calls)
	}
}

func TestSendPrompt_ContextTooLongSkipsRetries(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("context length exceeded, too long")}
	secondary := &mockClient{model: "claude-3", response: "ok"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(5))

	resp, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected secondary response, got %q", resp)
	}
	if primary.calls != 1 {
		t.Fatalf("expected primary called once (no retries for context too long), got %d", primary.calls)
	}
}

func TestSendPrompt_RetriesOnServerError(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("500 internal server error")}
	secondary := &mockClient{model: "claude-3", response: "ok"}

	maxRetries := 2
	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(maxRetries))

	resp, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected secondary response, got %q", resp)
	}
	// Should have retried: 1 initial + maxRetries
	expectedCalls := 1 + maxRetries
	if primary.calls != expectedCalls {
		t.Fatalf("expected primary called %d times (with retries), got %d", expectedCalls, primary.calls)
	}
}

func TestSendPrompt_CancelledContext(t *testing.T) {
	primary := &mockClient{model: "gpt-4", response: "should not see this"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := chain.SendPrompt(ctx, "hi", nil, 1000)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestSendPrompt_CooldownSkipsProvider(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("500 internal server error")}
	secondary := &mockClient{model: "claude-3", response: "ok"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(0), WithCooldown(1*time.Hour, 2*time.Hour, 2.0))

	// First call: primary fails, secondary succeeds, primary goes on cooldown
	_, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}

	// Reset calls counter
	primary.calls = 0
	secondary.calls = 0

	// Second call: primary should be skipped due to cooldown
	resp, err := chain.SendPrompt(context.Background(), "hi again", nil, 1000)
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected secondary response, got %q", resp)
	}
	if primary.calls != 0 {
		t.Fatalf("expected primary skipped (cooldown), got %d calls", primary.calls)
	}
}

func TestSendPromptWithTools_SuccessOnFirstProvider(t *testing.T) {
	expectedResp := &models.LLMResponse{Content: "tool result"}
	primary := &mockToolClient{
		mockClient:   mockClient{model: "gpt-4"},
		toolResponse: expectedResp,
	}
	secondary := &mockToolClient{
		mockClient:   mockClient{model: "claude-3"},
		toolResponse: &models.LLMResponse{Content: "secondary tool result"},
	}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeToolEntry("openai", "gpt-4", primary),
		makeToolEntry("claude", "claude-3", secondary),
	})

	tools := []models.ToolDefinition{{Type: "function", Function: models.ToolFunctionDef{Name: "test"}}}
	resp, err := chain.SendPromptWithTools(context.Background(), "hi", nil, tools, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "tool result" {
		t.Fatalf("expected primary tool response, got %q", resp.Content)
	}
	if primary.toolCalls != 1 {
		t.Fatalf("expected primary called once, got %d", primary.toolCalls)
	}
	if secondary.toolCalls != 0 {
		t.Fatalf("expected secondary not called, got %d", secondary.toolCalls)
	}
}

func TestSendPromptWithTools_FallbackOnFailure(t *testing.T) {
	primary := &mockToolClient{
		mockClient: mockClient{model: "gpt-4"},
		toolErr:    errors.New("500 internal server error"),
	}
	secondary := &mockToolClient{
		mockClient:   mockClient{model: "claude-3"},
		toolResponse: &models.LLMResponse{Content: "secondary"},
	}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeToolEntry("openai", "gpt-4", primary),
		makeToolEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(0))

	tools := []models.ToolDefinition{{Type: "function", Function: models.ToolFunctionDef{Name: "test"}}}
	resp, err := chain.SendPromptWithTools(context.Background(), "hi", nil, tools, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "secondary" {
		t.Fatalf("expected secondary response, got %q", resp.Content)
	}
}

func TestSendPromptWithTools_SkipsNonToolAwareClients(t *testing.T) {
	// First provider does NOT support tools
	nonToolClient := &mockClient{model: "ollama", response: "no tools"}
	// Second provider supports tools
	toolClient := &mockToolClient{
		mockClient:   mockClient{model: "gpt-4"},
		toolResponse: &models.LLMResponse{Content: "with tools"},
	}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("ollama", "llama2", nonToolClient),
		makeToolEntry("openai", "gpt-4", toolClient),
	})

	tools := []models.ToolDefinition{{Type: "function", Function: models.ToolFunctionDef{Name: "test"}}}
	resp, err := chain.SendPromptWithTools(context.Background(), "hi", nil, tools, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "with tools" {
		t.Fatalf("expected tool-aware response, got %q", resp.Content)
	}
	if nonToolClient.calls != 0 {
		t.Fatalf("non-tool client should not be called via SendPromptWithTools, got %d", nonToolClient.calls)
	}
}

func TestSendPromptWithTools_AllProvidersFail(t *testing.T) {
	primary := &mockToolClient{
		mockClient: mockClient{model: "gpt-4"},
		toolErr:    errors.New("500 internal server error"),
	}
	secondary := &mockToolClient{
		mockClient: mockClient{model: "claude-3"},
		toolErr:    errors.New("503 service unavailable"),
	}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeToolEntry("openai", "gpt-4", primary),
		makeToolEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(0))

	tools := []models.ToolDefinition{{Type: "function", Function: models.ToolFunctionDef{Name: "test"}}}
	_, err := chain.SendPromptWithTools(context.Background(), "hi", nil, tools, 1000)
	if err == nil {
		t.Fatal("expected error when all tool-aware providers fail")
	}
}

func TestSendPromptWithTools_NoToolAwareProviders(t *testing.T) {
	plain := &mockClient{model: "ollama", response: "plain"}
	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("ollama", "llama2", plain),
	})

	tools := []models.ToolDefinition{{Type: "function", Function: models.ToolFunctionDef{Name: "test"}}}
	_, err := chain.SendPromptWithTools(context.Background(), "hi", nil, tools, 1000)
	if err == nil {
		t.Fatal("expected error with no tool-aware providers")
	}
}

func TestGetModelName_ReturnsFirstAvailable(t *testing.T) {
	primary := &mockClient{model: "gpt-4"}
	secondary := &mockClient{model: "claude-3"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	})

	name := chain.GetModelName()
	if name != "gpt-4" {
		t.Fatalf("expected gpt-4, got %q", name)
	}
}

func TestGetModelName_SkipsCooldownProviders(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("500 error")}
	secondary := &mockClient{model: "claude-3"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(0), WithCooldown(1*time.Hour, 2*time.Hour, 2.0))

	// Trigger failure on primary to put it in cooldown
	_, _ = chain.SendPrompt(context.Background(), "hi", nil, 1000)

	name := chain.GetModelName()
	if name != "claude-3" {
		t.Fatalf("expected claude-3 (primary on cooldown), got %q", name)
	}
}

func TestGetModelName_NoProviders(t *testing.T) {
	chain := NewChain(newTestLogger(), nil)
	name := chain.GetModelName()
	if name != "unknown" {
		t.Fatalf("expected unknown, got %q", name)
	}
}

func TestGetHealth_ReturnsAllProviders(t *testing.T) {
	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", &mockClient{model: "gpt-4"}),
		makeEntry("claude", "claude-3", &mockClient{model: "claude-3"}),
	})

	health := chain.GetHealth()
	if len(health) != 2 {
		t.Fatalf("expected 2 health entries, got %d", len(health))
	}

	names := map[string]bool{}
	for _, h := range health {
		names[h.Name] = true
		if !h.Available {
			t.Fatalf("expected provider %s to be available initially", h.Name)
		}
	}
	if !names["openai"] || !names["claude"] {
		t.Fatalf("expected both provider names, got %v", names)
	}
}

func TestResetCooldowns(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("500 error")}
	secondary := &mockClient{model: "claude-3", response: "ok"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(0), WithCooldown(1*time.Hour, 2*time.Hour, 2.0))

	// Trigger failure to put primary on cooldown
	_, _ = chain.SendPrompt(context.Background(), "hi", nil, 1000)

	// Verify primary is on cooldown
	if chain.isAvailable("openai") {
		t.Fatal("expected openai to be on cooldown after failure")
	}

	// Reset cooldowns
	chain.ResetCooldowns()

	// Verify primary is available again
	if !chain.isAvailable("openai") {
		t.Fatal("expected openai to be available after ResetCooldowns")
	}

	health := chain.GetHealth()
	for _, h := range health {
		if h.ConsecutiveFails != 0 {
			t.Fatalf("expected consecutive fails reset to 0 for %s, got %d", h.Name, h.ConsecutiveFails)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		errMsg   string
		expected ErrorClass
	}{
		{"rate limit exceeded", ErrorClassRateLimit},
		{"429 too many requests", ErrorClassRateLimit},
		{"request timeout", ErrorClassTimeout},
		{"deadline exceeded", ErrorClassTimeout},
		{"401 unauthorized", ErrorClassAuth},
		{"403 forbidden", ErrorClassAuth},
		{"invalid api key", ErrorClassAuth},
		{"500 internal server error", ErrorClassServerError},
		{"502 bad gateway", ErrorClassServerError},
		{"503 service unavailable", ErrorClassServerError},
		{"model not found", ErrorClassModelNotFound},
		{"404 not found", ErrorClassModelNotFound},
		{"context length exceeded", ErrorClassContextTooLong},
		{"input too long", ErrorClassContextTooLong},
		{"max tokens exceeded", ErrorClassContextTooLong},
		{"some random error", ErrorClassUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.errMsg, func(t *testing.T) {
			got := ClassifyError(fmt.Errorf("%s", tt.errMsg))
			if got != tt.expected {
				t.Errorf("ClassifyError(%q) = %s, want %s", tt.errMsg, got, tt.expected)
			}
		})
	}
}

func TestClassifyError_Nil(t *testing.T) {
	got := ClassifyError(nil)
	if got != ErrorClassUnknown {
		t.Errorf("ClassifyError(nil) = %s, want unknown", got)
	}
}

func TestErrorClass_String(t *testing.T) {
	tests := []struct {
		class    ErrorClass
		expected string
	}{
		{ErrorClassUnknown, "unknown"},
		{ErrorClassRateLimit, "rate_limit"},
		{ErrorClassTimeout, "timeout"},
		{ErrorClassAuth, "auth_error"},
		{ErrorClassServerError, "server_error"},
		{ErrorClassModelNotFound, "model_not_found"},
		{ErrorClassContextTooLong, "context_too_long"},
	}
	for _, tt := range tests {
		if got := tt.class.String(); got != tt.expected {
			t.Errorf("%d.String() = %q, want %q", tt.class, got, tt.expected)
		}
	}
}

func TestWithOptions(t *testing.T) {
	chain := NewChain(newTestLogger(), nil,
		WithMaxRetries(5),
		WithCooldown(10*time.Second, 1*time.Minute, 3.0),
	)

	if chain.maxRetries != 5 {
		t.Fatalf("expected maxRetries=5, got %d", chain.maxRetries)
	}
	if chain.cooldownBase != 10*time.Second {
		t.Fatalf("expected cooldownBase=10s, got %v", chain.cooldownBase)
	}
	if chain.cooldownMax != 1*time.Minute {
		t.Fatalf("expected cooldownMax=1m, got %v", chain.cooldownMax)
	}
	if chain.cooldownFactor != 3.0 {
		t.Fatalf("expected cooldownFactor=3.0, got %v", chain.cooldownFactor)
	}
}

func TestCooldownExponentialBackoff(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("500 error")}
	secondary := &mockClient{model: "claude-3", response: "ok"}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(0), WithCooldown(1*time.Second, 1*time.Hour, 2.0))

	// Trigger first failure
	_, _ = chain.SendPrompt(context.Background(), "hi", nil, 1000)

	chain.mu.RLock()
	h := chain.health["openai"]
	firstCooldown := h.CooldownUntil
	fails1 := h.ConsecutiveFails
	chain.mu.RUnlock()

	if fails1 != 1 {
		t.Fatalf("expected 1 consecutive fail, got %d", fails1)
	}

	// Manually clear cooldown so we can trigger another failure
	chain.mu.Lock()
	chain.health["openai"].CooldownUntil = time.Time{}
	chain.mu.Unlock()

	primary.calls = 0
	chain.SendPrompt(context.Background(), "hi", nil, 1000)

	chain.mu.RLock()
	secondCooldown := chain.health["openai"].CooldownUntil
	fails2 := chain.health["openai"].ConsecutiveFails
	chain.mu.RUnlock()

	if fails2 != 2 {
		t.Fatalf("expected 2 consecutive fails, got %d", fails2)
	}

	// Second cooldown should be later (longer) than first
	if !secondCooldown.After(firstCooldown) {
		t.Fatalf("expected exponential backoff: second cooldown (%v) should be after first (%v)", secondCooldown, firstCooldown)
	}
}

func TestAuthError_LongerCooldown(t *testing.T) {
	primary := &mockClient{model: "gpt-4", err: errors.New("401 unauthorized")}
	secondary := &mockClient{model: "claude-3", response: "ok"}

	cooldownMax := 10 * time.Minute
	chain := NewChain(newTestLogger(), []FallbackEntry{
		makeEntry("openai", "gpt-4", primary),
		makeEntry("claude", "claude-3", secondary),
	}, WithMaxRetries(0), WithCooldown(1*time.Second, cooldownMax, 2.0))

	chain.SendPrompt(context.Background(), "hi", nil, 1000)

	chain.mu.RLock()
	h := chain.health["openai"]
	cooldownUntil := h.CooldownUntil
	chain.mu.RUnlock()

	// Auth error should get max cooldown
	expectedMin := time.Now().Add(cooldownMax - 1*time.Second)
	if cooldownUntil.Before(expectedMin) {
		t.Fatalf("auth error cooldown (%v) should be near max cooldown", cooldownUntil)
	}
}

func TestMarkSuccess_ResetsCooldown(t *testing.T) {
	// A provider that fails once then recovers should have its health reset
	callCount := 0
	dynamicClient := &dynamicMockClient{
		model: "gpt-4",
		fn: func() (string, error) {
			callCount++
			if callCount == 1 {
				return "", errors.New("500 error")
			}
			return "recovered", nil
		},
	}

	chain := NewChain(newTestLogger(), []FallbackEntry{
		{Provider: "openai", Model: "gpt-4", Client: dynamicClient},
	}, WithMaxRetries(1), WithCooldown(1*time.Nanosecond, 1*time.Nanosecond, 1.0))

	// The first attempt fails, the retry succeeds
	resp, err := chain.SendPrompt(context.Background(), "hi", nil, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "recovered" {
		t.Fatalf("expected recovered, got %q", resp)
	}

	chain.mu.RLock()
	h := chain.health["openai"]
	chain.mu.RUnlock()

	if h.ConsecutiveFails != 0 {
		t.Fatalf("expected consecutive fails reset to 0 after success, got %d", h.ConsecutiveFails)
	}
}

// dynamicMockClient allows different behavior per call.
type dynamicMockClient struct {
	model string
	fn    func() (string, error)
}

func (d *dynamicMockClient) GetModelName() string { return d.model }
func (d *dynamicMockClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return d.fn()
}
