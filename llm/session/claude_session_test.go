package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestClaudeSession_Basic(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	session := NewClaudeSession("test-api-key", "claude-3-5-sonnet-20241022", logger, 1, 0, 1024)

	if session.GetModelName() != "claude-3-5-sonnet-20241022" {
		t.Errorf("Expected model name to be 'claude-3-5-sonnet-20241022', got '%s'", session.GetModelName())
	}

	if session.IsSessionActive() {
		t.Error("Expected session to be inactive initially")
	}

	if session.GetSessionID() != "" {
		t.Errorf("Expected session ID to be empty, got '%s'", session.GetSessionID())
	}
}

func TestClaudeSession_MockedAPI(t *testing.T) {
	// Configure test server
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		// Claude Messages API
		if r.URL.Path == "/v1/messages" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
                                "id": "msg_test123",
                                "type": "message",
                                "role": "assistant",
                                "content": [
                                        {
                                                "type": "text",
                                                "text": "This is a test response from Claude"
                                        }
                                ],
                                "model": "claude-3-5-sonnet-20241022",
                                "conversation_id": "conv_test123"
                        }`))
		} else {
			t.Logf("Unhandled URL: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Create test ClaudeSession that uses our test server
	logger, _ := zap.NewDevelopment()

	// This is a minimal test that doesn't make real API calls
	session := &ClaudeSession{
		apiKey:      "test-api-key",
		model:       "claude-3-5-sonnet-20241022",
		logger:      logger,
		client:      server.Client(),
		maxAttempts: 1,
		backoff:     0,
		maxTokens:   1024,
	}

	// For actual API testing, we'd need more comprehensive mocks
	// This is just a basic validation that the struct is properly initialized
	if session.GetModelName() != "claude-3-5-sonnet-20241022" {
		t.Errorf("Expected model name to be 'claude-3-5-sonnet-20241022', got '%s'", session.GetModelName())
	}
}

func TestClaudeSession_Initialize(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	session := NewClaudeSession("test-api-key", "claude-3-5-sonnet-20241022", logger, 1, 0, 1024)

	err := session.InitializeSession(context.Background())
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if !session.IsSessionActive() {
		t.Error("Expected session to be active after initialization")
	}

	if session.GetSessionID() == "" {
		t.Error("Expected session ID to be set after initialization")
	}
}
