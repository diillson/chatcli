package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func testLogger() *zap.Logger {
	logger, _ := zap.NewDevelopment()
	return logger
}

func TestNewClient(t *testing.T) {
	c := NewClient("test-token", "gpt-4o", testLogger(), 3, 0)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.token != "test-token" {
		t.Errorf("expected token 'test-token', got '%s'", c.token)
	}
	if c.model != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got '%s'", c.model)
	}
	if c.baseURL != CopilotAPIBaseURL {
		t.Errorf("expected base URL '%s', got '%s'", CopilotAPIBaseURL, c.baseURL)
	}
}

func TestNewClient_CustomBaseURL(t *testing.T) {
	_ = os.Setenv("COPILOT_API_BASE_URL", "https://copilot-api.example.com")
	defer func() { _ = os.Unsetenv("COPILOT_API_BASE_URL") }()

	c := NewClient("token", "gpt-4o", testLogger(), 3, 0)
	if c.baseURL != "https://copilot-api.example.com" {
		t.Errorf("expected custom base URL, got '%s'", c.baseURL)
	}
}

func TestGetModelName(t *testing.T) {
	c := NewClient("token", "gpt-4o", testLogger(), 3, 0)
	name := c.GetModelName()
	if name == "" {
		t.Error("GetModelName returned empty string")
	}
}

func TestSendPrompt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify required headers
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Openai-Intent") != "conversation-edits" {
			t.Errorf("expected Openai-Intent: conversation-edits, got %s", r.Header.Get("Openai-Intent"))
		}
		if r.Header.Get("X-Initiator") != "user" {
			t.Errorf("expected X-Initiator: user, got %s", r.Header.Get("X-Initiator"))
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent header is empty")
		}

		// Verify payload
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if payload["model"] != "gpt-4o" {
			t.Errorf("expected model gpt-4o, got %v", payload["model"])
		}
		if payload["store"] != false {
			t.Errorf("expected store=false, got %v", payload["store"])
		}

		// Return valid response
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "Hello from Copilot!",
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient("test-token", "gpt-4o", testLogger(), 1, 0)
	c.baseURL = server.URL

	result, err := c.SendPrompt(context.Background(), "Hello", nil, 0)
	if err != nil {
		t.Fatalf("SendPrompt failed: %v", err)
	}
	if result != "Hello from Copilot!" {
		t.Errorf("expected 'Hello from Copilot!', got '%s'", result)
	}
}

func TestSendPrompt_WithHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []map[string]string `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}

		// System + user history + current prompt
		if len(payload.Messages) != 3 {
			t.Errorf("expected 3 messages, got %d", len(payload.Messages))
		}
		if payload.Messages[0]["role"] != "system" {
			t.Errorf("expected first message role 'system', got '%s'", payload.Messages[0]["role"])
		}

		resp := map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]interface{}{"content": "response"}},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient("token", "gpt-4o", testLogger(), 1, 0)
	c.baseURL = server.URL

	history := []models.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Previous question"},
	}

	_, err := c.SendPrompt(context.Background(), "New question", history, 0)
	if err != nil {
		t.Fatalf("SendPrompt with history failed: %v", err)
	}
}

func TestSendPrompt_403Forbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer server.Close()

	c := NewClient("expired-token", "gpt-4o", testLogger(), 1, 0)
	c.baseURL = server.URL

	_, err := c.SendPrompt(context.Background(), "Hello", nil, 0)
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !contains(err.Error(), "access denied") && !contains(err.Error(), "403") {
		t.Errorf("expected 403-related error, got: %v", err)
	}
}

func TestSendPrompt_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"choices": []map[string]interface{}{},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClient("token", "gpt-4o", testLogger(), 1, 0)
	c.baseURL = server.URL

	_, err := c.SendPrompt(context.Background(), "Hello", nil, 0)
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestCopilotAPIConstants(t *testing.T) {
	if CopilotAPIBaseURL != "https://api.githubcopilot.com" {
		t.Errorf("unexpected base URL: %s", CopilotAPIBaseURL)
	}
	if CopilotChatCompletionsPath != "/chat/completions" {
		t.Errorf("unexpected path: %s", CopilotChatCompletionsPath)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
