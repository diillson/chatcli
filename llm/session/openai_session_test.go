package session

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestOpenAISession_Basic(t *testing.T) {
	logger, _ := zap.NewDevelopment()

	session := NewOpenAISession("test-api-key", "gpt-4", logger, 1, 0)

	if session.GetModelName() != "gpt-4" {
		t.Errorf("Expected model name to be 'gpt-4', got '%s'", session.GetModelName())
	}

	if session.IsSessionActive() {
		t.Error("Expected session to be inactive initially")
	}

	if session.GetSessionID() != "" {
		t.Errorf("Expected session ID to be empty, got '%s'", session.GetSessionID())
	}
}

func TestOpenAISession_MockedAPI(t *testing.T) {
	// Configure test server
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++

		// Depending on the endpoint, return different responses
		switch r.URL.Path {
		case "/v1/assistants":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id": "asst_test123", "object": "assistant"}`))
		case "/v1/threads":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id": "thread_test123", "object": "thread"}`))
		case "/v1/threads/thread_test123/messages":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id": "msg_test123", "object": "thread.message"}`))
		case "/v1/threads/thread_test123/runs":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id": "run_test123", "object": "thread.run", "status": "queued"}`))
		case "/v1/threads/thread_test123/runs/run_test123":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"id": "run_test123", "object": "thread.run", "status": "completed"}`))
		default:
			if r.URL.Path == "/v1/threads/thread_test123/messages" && r.Method == http.MethodGet {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{
                                        "data": [
                                                {
                                                        "id": "msg_resp123",
                                                        "object": "thread.message",
                                                        "role": "assistant",
                                                        "content": [
                                                                {
                                                                        "type": "text",
                                                                        "text": {
                                                                                "value": "This is a test response",
                                                                                "annotations": []
                                                                        }
                                                                }
                                                        ]
                                                }
                                        ]
                                }`))
			} else {
				t.Logf("Unhandled URL: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}
	}))
	defer server.Close()

	// Create test OpenAISession that uses our test server
	logger, _ := zap.NewDevelopment()

	// This is a minimal test that doesn't make real API calls
	// In a real test, we would use http client with transport that redirects to our test server
	session := &OpenAISession{
		apiKey:      "test-api-key",
		model:       "gpt-4",
		logger:      logger,
		client:      server.Client(),
		maxAttempts: 1,
		backoff:     0,
	}

	// For actual API testing, we'd need more comprehensive mocks
	// This is just a basic validation that the struct is properly initialized
	if session.GetModelName() != "gpt-4" {
		t.Errorf("Expected model name to be 'gpt-4', got '%s'", session.GetModelName())
	}
}
